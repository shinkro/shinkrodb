package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/shinkro/shinkrodb/internal/app"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the full database update process",
	Long: `Run performs a complete update of the anime database:
1. Fetches anime IDs from MyAnimeList
2. Scrapes AniDB IDs from MAL pages
3. Maps TVDB IDs from AniDB IDs
4. Maps TMDB IDs for movies
5. Checks for duplicates
6. Creates TVDB mapping files

Fetch modes can be configured via --anidb and --tmdb flags or anidb_mode/tmdb_mode in config:
  - default: Default behavior (AniDB: only scrape for MAL IDs without AniDB ID, released in past 1 year, type = "tv"; TMDB: only fetch for movies without TMDB ID)
  - missing: Fetch all entries without ID (no filters)
  - all: Fetch everything, even if already has ID in cache
  - skip: Skip fetching entirely`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rootPath := viper.GetString("root_path")

		// Override AniDB mode from CLI flag if provided
		if anidbMode, _ := cmd.Flags().GetString("anidb"); anidbMode != "" {
			viper.Set("anidb_mode", anidbMode)
		}

		// Override TMDB mode from CLI flag if provided
		if tmdbMode, _ := cmd.Flags().GetString("tmdb"); tmdbMode != "" {
			viper.Set("tmdb_mode", tmdbMode)
		}

		// Initialize application
		application, err := app.NewApp()
		if err != nil {
			return fmt.Errorf("failed to initialize application: %w", err)
		}

		// Run the update process
		if err := application.Run(rootPath); err != nil {
			return fmt.Errorf("run failed: %w", err)
		}

		return nil
	},
}

func init() {
	runCmd.Flags().String("anidb", "", "AniDB fetch mode: 'default' (past year, tv only), 'missing' (all without AniDB ID), 'all' (everything), or 'skip' (skip fetching)")
	runCmd.Flags().String("tmdb", "", "TMDB fetch mode: 'default' (only movies without TMDB ID), 'missing' (all movies without TMDB ID), 'all' (everything), or 'skip' (skip fetching)")
	rootCmd.AddCommand(runCmd)
}

