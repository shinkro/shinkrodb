package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/shinkro/shinkrodb/internal/app"
)

var formatCmd = &cobra.Command{
	Use:   "format",
	Short: "Format YAML mapping files",
	Long: `Format the TMDB and TVDB master mapping YAML files
to ensure consistent formatting.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rootPath := viper.GetString("root_path")

		// Initialize application
		application, err := app.NewApp()
		if err != nil {
			return fmt.Errorf("failed to initialize application: %w", err)
		}

		// Format files
		if err := application.FormatFiles(rootPath); err != nil {
			return fmt.Errorf("format failed: %w", err)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(formatCmd)
}

