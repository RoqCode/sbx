package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"sbx/internal/app/pull"
)

type pullFlags struct {
	spaceID   int
	matchMode string
	all       bool
	dryRun    bool
}

func newPullCommand() *cobra.Command {
	flags := pullFlags{
		spaceID:   globalOpts.SourceSpaceID,
		matchMode: "exact",
	}

	cmd := &cobra.Command{
		Use:   "pull-components [name...]",
		Short: "Download component schemas and presets from a Storyblok space",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !flags.all {
				return fmt.Errorf("either provide component names or use --all")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("space") {
				flags.spaceID = globalOpts.SourceSpaceID
			}
			if globalOpts.Token == "" {
				return fmt.Errorf("management token is required (flag --token or SB_MGMT_TOKEN)")
			}
			if flags.spaceID <= 0 {
				return fmt.Errorf("a valid space ID is required (flag --space or SOURCE_SPACE_ID)")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			options := pull.Options{
				Token:     globalOpts.Token,
				SpaceID:   flags.spaceID,
				Names:     args,
				MatchMode: flags.matchMode,
				All:       flags.all,
				OutDir:    globalOpts.OutDir,
				DryRun:    flags.dryRun,
			}

			result, err := pull.Run(cmd.Context(), options)
			if err != nil {
				code := result.ExitCode
				if code == 0 {
					code = ExitCodeExecution
				}
				SetExitCode(code)
				return err
			}

			SetExitCode(result.ExitCode)
			return nil
		},
	}

	cmd.Flags().IntVar(&flags.spaceID, "space", flags.spaceID, "Space ID to pull from (defaults to SOURCE_SPACE_ID)")
	cmd.Flags().StringVar(&flags.matchMode, "match", flags.matchMode, "Component name matching mode: exact, prefix, glob")
	cmd.Flags().BoolVar(&flags.all, "all", false, "Pull all components")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Print planned actions without writing files")

	return cmd
}
