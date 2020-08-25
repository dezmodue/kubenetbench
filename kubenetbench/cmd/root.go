package cmd

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kkourt/kubenetbench/kubenetbench/core"
)

var (
	quiet       bool
	sessID      string
	sessDirBase string
)

// var noCleanup bool

var rootCmd = &cobra.Command{
	Use:   "kubenetbench",
	Short: "kubenetbench is a k8s network benchmark",
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "initalize a seasson",
	Run: func(cmd *cobra.Command, args []string) {
		sess, err := core.InitSession(sessID, sessDirBase)
		if err != nil {
			log.Fatal(fmt.Sprintf("error initializing session: %w", err))
		}
		InitLog(sess)
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&sessID, "session-id", "s", "", "session id")
	rootCmd.MarkPersistentFlagRequired("session-id")
	rootCmd.PersistentFlags().StringVarP(&sessDirBase, "session-base-dir", "d", ".", "base directory to store session data")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "quiet output")

	// misc commands
	rootCmd.AddCommand(initCmd)

	// benchmark commands
	rootCmd.AddCommand(pod2podCmd)
	rootCmd.AddCommand(serviceCmd)
}

// return a session based on the given flags
func getSession() *core.Session {
	sess, err := core.NewSession(sessID, sessDirBase)
	if err != nil {
		log.Fatal(fmt.Errorf("error creating session: %w", err))
	}

	InitLog(sess)
	return sess
}

func InitLog(sess *core.Session) {
	f, err := sess.OpenLog()
	if err != nil {
		log.Fatal(fmt.Sprintf("error openning session log file: %w", err))
	}

	if quiet {
		log.SetOutput(f)
	} else {
		m := io.MultiWriter(f, os.Stdout)
		log.SetOutput(m)
	}
	log.Printf("****** %s\n", strings.Join(os.Args, " "))
}

// Execute runs the main (root) command
func Execute() error {
	return rootCmd.Execute()
}
