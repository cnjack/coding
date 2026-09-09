package command

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cnjack/jcode/internal/config"
	util "github.com/cnjack/jcode/internal/util"
)

// NewTrustCmd builds `jcode trust`: the user-visible confirmation that a
// project's AGENTS.md instructions may load. Trust is persisted OUTSIDE the
// repository (~/.jcode/project_trust.json), keyed on the project root, so
// repository content can never grant itself trust.
func NewTrustCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust [path]",
		Short: "Trust a project so its AGENTS.md instructions load",
		Long: "Mark a project as trusted so project-level AGENTS.md files are loaded into\n" +
			"the system prompt. By default new/unknown projects are untrusted and their\n" +
			"AGENTS.md is ignored (it is repository-controlled content and a hostile\n" +
			"clone could otherwise inject instructions).\n\n" +
			"The decision is stored in ~/.jcode/project_trust.json, never inside the\n" +
			"repository, and takes effect for new sessions / prompt rebuilds.",
		Args: cobra.MaximumNArgs(1),
		RunE: runTrust,
	}
	cmd.Flags().Bool("status", false, "Show the trust decision for the current (or given) project without changing it")
	return cmd
}

// NewUntrustCmd builds `jcode untrust`: revoke a project's trust. Project
// AGENTS.md stops loading on the next prompt build.
func NewUntrustCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "untrust [path]",
		Short: "Revoke project trust (project AGENTS.md stops loading)",
		Long: "Remove a project from the trust store. Its project-level AGENTS.md files\n" +
			"are excluded from the system prompt again, effective for new sessions /\n" +
			"prompt rebuilds.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pwd := targetPath(args)
			if err := config.UntrustProjectRoot(pwd); err != nil {
				return err
			}
			fmt.Printf("Revoked trust for project %s\n", config.ProjectInstructionsAllowed(pwd).Root)
			fmt.Println("Project AGENTS.md instructions will no longer load (new sessions / prompt rebuilds).")
			return nil
		},
	}
}

func targetPath(args []string) string {
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}
	return util.GetWorkDir()
}

func runTrust(cmd *cobra.Command, args []string) error {
	pwd := targetPath(args)

	if status, _ := cmd.Flags().GetBool("status"); status {
		printTrustStatus(pwd)
		return nil
	}

	decision := config.ProjectInstructionsAllowed(pwd)
	fmt.Printf("Trusting project root: %s\n", decision.Root)
	fmt.Println("Project-level AGENTS.md files will load for new sessions of this project.")
	if err := config.TrustProjectRoot(pwd); err != nil {
		return err
	}
	fmt.Println("Done. (Revoke any time with: jcode untrust)")
	return nil
}

func printTrustStatus(pwd string) {
	decision := config.ProjectInstructionsAllowed(pwd)
	fmt.Printf("Project:      %s\n", pwd)
	fmt.Printf("Root:         %s\n", decision.Root)
	switch decision.Reason {
	case "env":
		fmt.Println("Instructions: LOADED (JCODE_AGENTS_TRUST_PROJECT=1 overrides per-project trust)")
	case "store":
		fmt.Println("Instructions: LOADED (project is trusted; revoke with `jcode untrust`)")
	default:
		fmt.Println("Instructions: SKIPPED (untrusted project; run `jcode trust` to load AGENTS.md)")
	}
	if _, err := os.Stat(config.ProjectTrustStorePath()); err == nil {
		fmt.Printf("Trust store:  %s\n", config.ProjectTrustStorePath())
	}
}
