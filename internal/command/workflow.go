package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/cnjack/jcode/internal/config"
	"github.com/cnjack/jcode/internal/flow"
	internalmodel "github.com/cnjack/jcode/internal/model"
	"github.com/cnjack/jcode/internal/tools"
	utils "github.com/cnjack/jcode/internal/util"
)

// NewWorkflowCmd builds the `jcode workflow` (alias `flow`) command tree for
// listing, inspecting, and running dynamic workflows from the terminal.
func NewWorkflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workflow",
		Aliases: []string{"workflows", "flow"},
		Short:   "Manage and run dynamic workflows (multi-agent orchestration scripts)",
	}
	cmd.AddCommand(
		newFlowListCmd(),
		newFlowShowCmd(),
		newFlowValidateCmd(),
		newFlowRunCmd(),
	)
	return cmd
}

func newFlowValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <name|file.js>",
		Short: "Check that a workflow parses and compiles, without running it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			var src string
			if isFlowFile(target) {
				data, err := os.ReadFile(target)
				if err != nil {
					return fmt.Errorf("read workflow file: %w", err)
				}
				src = string(data)
			} else {
				wf, ok := newFlowLoader().Get(target)
				if !ok {
					return fmt.Errorf("workflow %q not found", target)
				}
				src = wf.Source
			}
			if err := flow.Validate(src); err != nil {
				return err
			}
			meta, _ := flow.ParseMeta(src)
			fmt.Printf("✓ %s is valid (%d phase(s))\n", meta.Name, len(meta.Phases))
			return nil
		},
	}
}

func newFlowLoader() *flow.Loader {
	l := flow.NewLoader()
	if pwd, err := os.Getwd(); err == nil {
		l.LoadProject(pwd)
	}
	return l
}

func newFlowListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available workflows (builtin + user + project)",
		RunE: func(cmd *cobra.Command, args []string) error {
			l := newFlowLoader()
			all := l.All()
			if len(all) == 0 {
				fmt.Println("No workflows found. Add .js files under ~/.jcode/workflows/ or <project>/.jcode/workflows/.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tSCOPE\tPHASES\tDESCRIPTION")
			for _, wf := range all {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", wf.Meta.Name, wf.Scope, len(wf.Meta.Phases), wf.Meta.Description)
			}
			return w.Flush()
		},
	}
}

func newFlowShowCmd() *cobra.Command {
	var src bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show a workflow's metadata (and --source for the script)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l := newFlowLoader()
			wf, ok := l.Get(args[0])
			if !ok {
				return fmt.Errorf("workflow %q not found", args[0])
			}
			fmt.Printf("Name:        %s\nScope:       %s\nDescription: %s\nWhen to use: %s\nPath:        %s\n",
				wf.Meta.Name, wf.Scope, wf.Meta.Description, wf.Meta.WhenToUse, nz(wf.Path))
			if len(wf.Meta.Phases) > 0 {
				fmt.Println("Phases:")
				for _, p := range wf.Meta.Phases {
					fmt.Printf("  - %s: %s\n", p.Title, p.Detail)
				}
			}
			if src {
				fmt.Printf("\n--- source ---\n%s\n", wf.Source)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&src, "source", false, "Also print the workflow script source")
	return cmd
}

func newFlowRunCmd() *cobra.Command {
	var argsJSON string
	var budget int64
	var timeout time.Duration
	var concurrency int
	cmd := &cobra.Command{
		Use:   "run <name|file.js>",
		Short: "Run a workflow to completion and print its result",
		Long: "Run a saved workflow by name, or a workflow script by file path. " +
			"Progress is streamed to stderr; the final result is printed to stdout.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// Resolve the workflow: a file path, or a saved name.
			var wf flow.Workflow
			target := args[0]
			if isFlowFile(target) {
				data, err := os.ReadFile(target)
				if err != nil {
					return fmt.Errorf("read workflow file: %w", err)
				}
				if err := flow.Validate(string(data)); err != nil {
					return fmt.Errorf("invalid workflow %s: %w", target, err)
				}
				meta, _ := flow.ParseMeta(string(data)) // already validated above
				wf = flow.Workflow{Meta: meta, Source: string(data), Path: target, Scope: flow.ScopeInline}
			} else {
				l := newFlowLoader()
				got, ok := l.Get(target)
				if !ok {
					return fmt.Errorf("workflow %q not found (try `jcode flow list`)", target)
				}
				wf = got
			}

			// Parse --args JSON.
			var runArgs interface{}
			if strings.TrimSpace(argsJSON) != "" {
				if err := json.Unmarshal([]byte(argsJSON), &runArgs); err != nil {
					return fmt.Errorf("invalid --args JSON: %w", err)
				}
			}

			pwd, _ := os.Getwd()
			spawn, resolver, err := buildFlowSpawn(ctx, pwd)
			if err != nil {
				return err
			}

			engOpts := []flow.Option{flow.WithResolver(resolver)}
			if timeout > 0 {
				engOpts = append(engOpts, flow.WithTimeout(timeout))
			}
			if concurrency > 0 {
				engOpts = append(engOpts, flow.WithConcurrency(concurrency))
			}
			eng := flow.New(spawn, newStderrSink(), engOpts...)
			fmt.Fprintf(os.Stderr, "▶ running workflow %q…\n", wf.Meta.Name)
			result, runErr := eng.Run(ctx, wf, flow.RunOptions{Args: runArgs, BudgetTotal: budget})
			if runErr != nil {
				return fmt.Errorf("workflow failed: %w", runErr)
			}
			printFlowResult(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&argsJSON, "args", "", "JSON value passed to the workflow as the `args` global")
	cmd.Flags().Int64Var(&budget, "budget", 0, "Token budget target exposed as budget.total (0 = unset)")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "Per-run wall-clock timeout (e.g. 90m); 0 uses the engine default (30m)")
	cmd.Flags().IntVar(&concurrency, "concurrency", 0, "Max concurrent agents (0 uses the engine default of 16)")
	return cmd
}

func isFlowFile(s string) bool {
	if strings.HasSuffix(s, ".js") {
		return true
	}
	if info, err := os.Stat(s); err == nil && !info.IsDir() {
		return true
	}
	return false
}

// buildFlowSpawn constructs the real SpawnFunc (Env + model factory from config)
// and a resolver for nested workflow() calls.
func buildFlowSpawn(ctx context.Context, pwd string) (flow.SpawnFunc, func(string) (flow.Workflow, bool), error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	providerName, modelName := cfg.GetProviderModel()
	providers := cfg.GetProviders()
	providerCfg := providers[providerName]
	if providerCfg == nil {
		return nil, nil, fmt.Errorf("provider %q not found in config", providerName)
	}
	registry := internalmodel.NewModelRegistryWithConfig(cfg)
	baseURL := providerCfg.BaseURL
	if baseURL == "" {
		baseURL = registry.GetProviderAPI(providerName)
	}
	effortCfg := *providerCfg
	effortCfg.ReasoningEffort = config.ResolveEffort(providerName, modelName, providerCfg.ReasoningEffort)
	chatModel, err := internalmodel.NewChatModelFromProvider(ctx, providerName, modelName, baseURL, &effortCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create model: %w", err)
	}
	factory := internalmodel.NewModelFactory(cfg, chatModel)

	platform := utils.GetSystemInfo()
	// Shared managed deny-read policy (same object as every other Env).
	tools.InitManagedDenyRead(cfg)
	env := tools.NewEnv(pwd, platform)

	spawn := tools.NewFlowSpawn(tools.FlowSpawnDeps{
		Env:          env,
		ModelFactory: factory,
	})
	loader := newFlowLoader()
	return spawn, loader.Resolve, nil
}

// newStderrSink prints run progress to stderr (so stdout stays the result).
func newStderrSink() flow.EventSink {
	return flow.FuncSink{
		Phase: func(_ string, title, detail string) {
			if detail != "" {
				fmt.Fprintf(os.Stderr, "  ▸ %s — %s\n", title, detail)
			} else {
				fmt.Fprintf(os.Stderr, "  ▸ %s\n", title)
			}
		},
		AgentStart: func(_ string, a flow.AgentEvent) {
			fmt.Fprintf(os.Stderr, "    ⟳ %s\n", a.Label)
		},
		AgentDone: func(_ string, a flow.AgentEvent) {
			if a.Status == "failed" {
				fmt.Fprintf(os.Stderr, "    ✗ %s — %s\n", a.Label, a.Err)
			} else {
				fmt.Fprintf(os.Stderr, "    ✓ %s (%d tok)\n", a.Label, a.Tokens)
			}
		},
		Log: func(_ string, level, msg string) {
			fmt.Fprintf(os.Stderr, "    · %s\n", msg)
		},
	}
}

func printFlowResult(result interface{}) {
	switch v := result.(type) {
	case nil:
		// no return value
	case string:
		fmt.Println(v)
	default:
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			fmt.Println(string(b))
		} else {
			fmt.Printf("%v\n", v)
		}
	}
}
