package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xs23933/core/v3/gen"
)

// Release builds may override this with -ldflags '-X main.coreVersion=v3.x.y'.
var coreVersion = "v3.1.71"

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{Use: "corectl", Short: "Core project and enum generator", SilenceUsage: true, SilenceErrors: true}
	cfg := ProjectConfig{GoVersion: "1.25.0", CoreVersion: coreVersion, ProjectType: "http", OutputDir: "."}
	create := &cobra.Command{
		Use: "new [project-name]", Args: cobra.ExactArgs(1), Short: "Create a project with cmd → internal/app assembly",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.ProjectName = args[0]
			if cfg.ModulePath == "" {
				cfg.ModulePath = cfg.ProjectName
			}
			if err := CreateProject(&cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s\nRead %s for generation, migration and startup steps.\n", filepath.Join(cfg.OutputDir, cfg.ProjectName), filepath.Join(cfg.OutputDir, cfg.ProjectName, "README.md"))
			return nil
		},
	}
	flags := create.Flags()
	flags.StringVarP(&cfg.ModulePath, "module", "m", "", "Go module path (defaults to project name)")
	flags.StringVar(&cfg.GoVersion, "go-version", cfg.GoVersion, "Go language version (>= 1.25.0)")
	flags.StringVar(&cfg.CoreVersion, "core-version", cfg.CoreVersion, "concrete Core v3 version")
	flags.StringVarP(&cfg.ProjectType, "type", "t", cfg.ProjectType, "http or grpc")
	flags.StringVarP(&cfg.OutputDir, "output", "o", cfg.OutputDir, "output parent directory")
	generate := &cobra.Command{Use: "gen", Short: "Generate source files"}
	check := false
	enums := &cobra.Command{
		Use: "enums [directory]", Args: cobra.MaximumNArgs(1), Short: "Generate enums beside *_enums.yaml and enums.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			if err := gen.GenerateEnumsInDir(dir, check); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Enum files are up to date.")
			return nil
		},
	}
	enums.Flags().BoolVar(&check, "check", false, "check without writing; fail on missing or outdated outputs")
	generate.AddCommand(enums)
	root.AddCommand(create, generate, &cobra.Command{Use: "version", Args: cobra.NoArgs, Run: func(cmd *cobra.Command, _ []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "corectl template baseline: Core %s\n", coreVersion)
	}})
	return root
}
