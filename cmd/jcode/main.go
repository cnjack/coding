package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cnjack/jcode/internal/browser"
	"github.com/cnjack/jcode/internal/command"
)

func main() {
	// Native-messaging launch: Chrome/Edge start `jcode chrome-extension://<id>/`
	// when the browser extension calls connectNative. Handle it before cobra —
	// this mode speaks the stdio native-messaging protocol and must not print
	// anything else to stdout.
	if browser.MaybeRunNativeHost(os.Args[1:]) {
		return
	}

	var (
		prompt     string
		resumeUUID string
		agentName  string
		unsafeMode bool
	)

	rootCmd := &cobra.Command{
		Use:           "jcode",
		Short:         "JCODE — AI coding assistant",
		Version:       command.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return command.RunInteractive(prompt, resumeUUID, agentName, unsafeMode)
		},
	}
	rootCmd.SetVersionTemplate(fmt.Sprintf("JCODE — Coding Assistant\nVersion:    %s\nBuild time: %s\nGit commit: %s\n", command.Version, command.BuildTime, command.GitCommit))
	rootCmd.Flags().StringVarP(&prompt, "prompt", "p", "", "One-shot prompt (non-interactive)")
	rootCmd.Flags().StringVar(&resumeUUID, "resume", "", "Resume a previous session by UUID")
	rootCmd.Flags().StringVar(&agentName, "agent", "", "Custom agent for the current session")
	rootCmd.Flags().BoolVar(&unsafeMode, "unsafe", false, "Auto-approve all tool calls (overrides config)")

	rootCmd.AddCommand(
		command.NewMCPCmd(),
		command.NewACPCmd(),
		command.NewWebCmd(),
		command.NewAutomationCmd(),
		command.NewWorkflowCmd(),
		command.NewVersionCmd(),
		command.NewDoctorCmd(),
		command.NewSessionsCmd(),
		command.NewUpdateCmd(),
		command.NewMemoryCmd(),
		command.NewLoginCmd(),
		command.NewLogoutCmd(),
		command.NewCloudCmd(),
		command.NewTrustCmd(),
		command.NewUntrustCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
