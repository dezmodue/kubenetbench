package core

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"text/template"
	"time"

	"google.golang.org/grpc"

	pb "github.com/cilium/kubenetbench/benchmonitor/api"
)

const (
	monitorPort     = "8451"
	monitorSelector = "role=monitor"
)

var monitorTemplate = template.Must(template.New("monitor").Parse(`apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: knb-monitor
  labels:
    {{.sessLabel}}
    role: monitor
spec:
  selector:
    matchLabels:
      {{.sessLabel}}
      role: monitor
  template:
    metadata:
      labels:
        {{.sessLabel}}
        role: monitor
    spec:
      tolerations:
			- operator: Exists
      # # this toleration is to have the daemonset runnable on master nodes
      # # remove it if your masters can't run pods
      # - key: node-role.kubernetes.io/master
      #   effect: NoSchedule

      #
      hostNetwork: true
      hostPID: true
      hostIPC: true

      containers:
      - name: kubenetbench-monitor
        image: docker.io/cilium/kubenetbench-monitor
        securityContext:
           privileged: true
           capabilities:
              add:
                 # - NET_ADMIN
                 - SYS_ADMIN
        ports:
           - containerPort: 8451
             hostPort: 8451
        volumeMounts:
        - name: host
          mountPath: /host
          readOnly: true
      volumes:
      - name: host
        hostPath:
          path: /
`))

func (s *Session) genMonitorYaml() (string, error) {
	yaml := fmt.Sprintf("%s/monitor.yaml", s.dir)
	log.Printf("Generating %s", yaml)
	f, err := os.Create(yaml)
	if err != nil {
		return "", err
	}

	vals := map[string]interface{}{
		"sessLabel": s.getSessionLabel(": "),
	}
	err = monitorTemplate.Execute(f, vals)
	if err != nil {
		return "", err
	}
	f.Close()
	return yaml, nil
}

type FileReceiver interface {
	Recv() (*pb.File, error)
}

func copyStreamToFile(fname string, stream FileReceiver) error {

	f, err := os.OpenFile(fname, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		data, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("io error: %w", err)
		}

		_, err = f.Write(data.Data)
		if err != nil {
			return fmt.Errorf("Error writing data: %w", err)
		}
	}

	return nil
}

func (s *Session) srvAddrForNode(ctx context.Context, nodeName string) (string, error) {
	var host, port string
	if !s.portForward {
		// directly connect to node IP if port-forwarding is disabled
		nodeIP, err := KubeGetNodeIP(nodeName)
		if err != nil {
			return "", err
		}
		host = nodeIP
		port = monitorPort
	} else {
		monitorPod, err := s.KubeGetPodForNode(nodeName, monitorSelector)
		if err != nil {
			return "", err
		}

		port, err = KubePortForward(ctx, monitorPod, monitorPort)
		if err != nil {
			return "", err
		}

		host = "localhost"
	}

	return net.JoinHostPort(host, port), nil
}

func (s *Session) DialMonitor(ctx context.Context, nodeName string) (*grpc.ClientConn, error) {
	srvAddr, err := s.srvAddrForNode(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain monitor address of node %s: %w", nodeName, err)
	}

	conn, err := grpc.Dial(srvAddr, grpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to monitor %s: %w", srvAddr, err)
	}

	return conn, err
}

func (s *Session) GetSysInfoNode(node_name, node_ip string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := s.DialMonitor(ctx, node_name)
	if err != nil {
		return err
	}
	defer conn.Close()

	cli := pb.NewKubebenchMonitorClient(conn)
	stream, err := cli.GetSysInfo(ctx, &pb.Empty{})
	if err != nil {
		return fmt.Errorf("failed to retrieve sysinfo from monitor on %q: %w", node_name, err)
	}

	fname := fmt.Sprintf("%s/%s.sysinfo", s.dir, node_name)
	return copyStreamToFile(fname, stream)
}

func (s *Session) GetSysInfoNodes() error {

	lines, err := KubeGetNodesAndIps()
	if err != nil {
		return err
	}

	errstr := ""
	retriesOrig := 10
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			log.Fatal("filed to parse  line %s", line)
		}
		node_name := fields[0]
		node_ip := fields[1]
		retries := retriesOrig
		for {
			log.Printf("calling GetSysInfoNode on %s/%s (remaining retries: %d)", node_name, node_ip, retries)
			err = s.GetSysInfoNode(node_name, node_ip)
			if err == nil {
				break
			}

			if retries == 0 {
				err := fmt.Sprintf("Error calling GetSysInfoNode %s after %d retries (last error:%w)", node_name, retriesOrig, err)
				errstr = errstr + "\n" + err
				break
			}

			retries--
			time.Sleep(4 * time.Second)
		}
	}

	if len(errstr) == 0 {
		return nil
	} else {
		return fmt.Errorf("GetSysInfoNodes() failed:\n%s", errstr)
	}
}

func (r *RunBenchCtx) endCollection() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var err error = nil

	for _, node := range r.collectNodes {
		conn, err := r.session.DialMonitor(ctx, node)
		if err != nil {
			return err
		}
		defer conn.Close()
		cli := pb.NewKubebenchMonitorClient(conn)
		conf := &pb.CollectionResultsConf{
			CollectionId: r.runid,
		}

		stream, err := cli.GetCollectionResults(ctx, conf)
		if err != nil {
			log.Printf("collection on monitor %s failed: %s\n", node, err)
		}

		fname := fmt.Sprintf("%s/perf-%s.tar.bz2", r.getDir(), node)
		err = copyStreamToFile(fname, stream)
		if err != nil {
			log.Printf("writing collection data from node %s failed: %s\n", node, err)
		} else {
			log.Printf("perf data for %s can be found in: %s\n", node, fname)
		}
	}

	return err
}

func (r *RunBenchCtx) startCollection() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	labels := [...]string{PodName, PodNodeName, PodPhase}
	podsinfo, err := r.KubeGetPods__(labels[:])
	if err != nil {
		return err
	}

	nodes := make(map[string]struct{})
	log.Printf("Pods: \n")
	for _, a := range podsinfo {
		log.Printf(" %v\n", a)
		nodes[a[1]] = struct{}{}
	}

	for node, _ := range nodes {
		conn, err := r.session.DialMonitor(ctx, node)
		if err != nil {
			return err
		}
		defer conn.Close()
		//log.Printf("connected to monitor on %s\n", node)
		cli := pb.NewKubebenchMonitorClient(conn)
		conf := &pb.CollectionConf{
			Duration:     "5",
			CollectionId: r.runid,
		}

		_, err = cli.StartCollection(context.Background(), conf)
		if err == nil {
			log.Printf("started collection on monitor %s\n", node)
			r.collectNodes = append(r.collectNodes, node)
		} else {
			log.Printf("started collection on monitor %s failed: %s\n", node, err)
		}
	}

	return nil
}
