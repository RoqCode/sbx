package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

const (
	// ExitCodeOK indicates success.
	ExitCodeOK = 0
	// ExitCodeInvalid indicates invalid input or missing entities.
	ExitCodeInvalid = 1
	// ExitCodeAPI indicates Storyblok API errors.
	ExitCodeAPI = 2
	// ExitCodeExecution covers unexpected execution failures.
	ExitCodeExecution = 3
)

var (
	rootCmd = &cobra.Command{
		Use:           "sbx",
		Short:         "Storyblok component sync utility",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	globalOpts GlobalOptions
	exitCode   = ExitCodeOK
)

// GlobalOptions carries configuration shared across commands.
type GlobalOptions struct {
	Token         string
	SourceSpaceID int
	TargetSpaceID int
	OutDir        string
}

// Execute runs the root command tree and returns an exit code for os.Exit.
func Execute() int {
	if err := rootCmd.Execute(); err != nil {
		// If no exit code was set, surface a generic execution failure.
		if exitCode == ExitCodeOK {
			fmt.Fprintln(os.Stderr, err)
			return ExitCodeExecution
		}
		fmt.Fprintln(os.Stderr, err)
		return exitCode
	}
	return exitCode
}

func init() {
	defaultToken := os.Getenv("SB_MGMT_TOKEN")
	defaultSource := envInt("SOURCE_SPACE_ID", 0)
	defaultTarget := envInt("TARGET_SPACE_ID", 0)
	defaultOut := defaultString(os.Getenv("SBX_OUT_DIR"), "component-schemas/")

	globalOpts.Token = defaultToken
	globalOpts.SourceSpaceID = defaultSource
	globalOpts.TargetSpaceID = defaultTarget
	globalOpts.OutDir = defaultOut

	rootCmd.PersistentFlags().StringVar(&globalOpts.Token, "token", defaultToken, "Storyblok management token (env: SB_MGMT_TOKEN)")
	rootCmd.PersistentFlags().IntVar(&globalOpts.SourceSpaceID, "source-space", defaultSource, "Source space ID (env: SOURCE_SPACE_ID)")
	rootCmd.PersistentFlags().IntVar(&globalOpts.TargetSpaceID, "target-space", defaultTarget, "Target space ID (env: TARGET_SPACE_ID)")
	rootCmd.PersistentFlags().StringVar(&globalOpts.OutDir, "out", defaultOut, "Output directory for component schemas (env: SBX_OUT_DIR)")

	// Inject subcommands
	rootCmd.AddCommand(newPullCommand())
	rootCmd.AddCommand(newPushCommand())
	rootCmd.AddCommand(newCompletionCommand())
}

// SetExitCode allows subcommands to override the process exit code.
func SetExitCode(code int) {
	if code > exitCode {
		exitCode = code
	}
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// Global returns a snapshot of global options for consumers.
func Global() GlobalOptions {
	return globalOpts
}
