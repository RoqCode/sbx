package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"sbx/internal/app/push"
)

type pushFlags struct {
	spaceID   int
	matchMode string
	all       bool
	dryRun    bool
	dir       string
}

func newPushCommand() *cobra.Command {
	flags := pushFlags{
		spaceID:   globalOpts.TargetSpaceID,
		matchMode: "exact",
		dir:       globalOpts.OutDir,
	}

	cmd := &cobra.Command{
		Use:   "push-components [name...]",
		Short: "Upload component schemas and presets to a Storyblok space",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !flags.all {
				return fmt.Errorf("either provide component names or use --all")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("space") {
				flags.spaceID = globalOpts.TargetSpaceID
			}
			if !cmd.Flags().Changed("dir") {
				flags.dir = globalOpts.OutDir
			}
			if globalOpts.Token == "" {
				return fmt.Errorf("management token is required (flag --token or SB_MGMT_TOKEN)")
			}
			if flags.spaceID <= 0 {
				return fmt.Errorf("a valid space ID is required (flag --space or TARGET_SPACE_ID)")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			options := push.Options{
				Token:     globalOpts.Token,
				SpaceID:   flags.spaceID,
				Names:     args,
				MatchMode: flags.matchMode,
				All:       flags.all,
				Dir:       flags.dir,
				DryRun:    flags.dryRun,
			}

			result, err := push.Run(cmd.Context(), options)
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

	cmd.Flags().IntVar(&flags.spaceID, "space", flags.spaceID, "Space ID to push to (defaults to TARGET_SPACE_ID)")
	cmd.Flags().StringVar(&flags.matchMode, "match", flags.matchMode, "Component name matching mode: exact, prefix, glob")
	cmd.Flags().BoolVar(&flags.all, "all", false, "Push all components found in the directory")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Print planned actions without writing to Storyblok")
	cmd.Flags().StringVar(&flags.dir, "dir", flags.dir, "Directory containing component schemas to push")

	return cmd
}
