package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/shinkro/shinkrodb/internal/app"
)

var genmapCmd = &cobra.Command{
	Use:   "genmap",
	Short: "Generate mapping files from master files",
	Long: `Generate mapping files from master YAML files.
This command reads the master mapping files and generates
the final mapping files used by shinkro.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rootPath := viper.GetString("root_path")

		// Initialize application
		application, err := app.NewApp()
		if err != nil {
			return fmt.Errorf("failed to initialize application: %w", err)
		}

		// Generate mappings
		if err := application.GenerateMappings(rootPath); err != nil {
			return fmt.Errorf("generate mappings failed: %w", err)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(genmapCmd)
}

