package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/shinkro/shinkrodb/internal/cache"
	"github.com/shinkro/shinkrodb/internal/config"
	"github.com/shinkro/shinkrodb/internal/database"
	"github.com/shinkro/shinkrodb/internal/domain"
	"github.com/shinkro/shinkrodb/internal/logger"
	"github.com/shinkro/shinkrodb/internal/mal"
	"github.com/shinkro/shinkrodb/internal/repository"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate-cache",
	Short: "Migrate existing HTML cache to SQLite database",
	Long: `Migrate existing Colly HTML cache files to a new SQLite-based cache.
This is a one-time migration command that reads all HTML files from the cache directory,
extracts MAL IDs and AniDB IDs, and stores them in shinkrodb.db.

After migration, you can use the new efficient cache system.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cacheDir, _ := cmd.Flags().GetString("cache-dir")
		rootPath := viper.GetString("root_path")

		// Create database path in current directory (./) instead of root-path
		dbPath := filepath.Join(".", "shinkrodb.db")

		log := logger.NewLogger()

		log.Info().
			Str("cache_dir", cacheDir).
			Str("root_path", rootPath).
			Str("db_path", dbPath).
			Msg("Starting cache migration")

		// Initialize repository to get anime data
		animeRepo := repository.NewFileRepository(log)
		paths := domain.NewPaths(rootPath)

		// Only fetch MAL IDs if cache-dir is provided (needed for release dates/types in migration)
		if cacheDir != "" {
			// Initialize database and cache repository first
			db, err := database.NewDB(".", log)
			if err != nil {
				return fmt.Errorf("failed to initialize database: %w", err)
			}
			defer db.Close()

			cacheRepo := database.NewCacheRepo(log, db)

			// Load configuration
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Fetch MAL IDs first (needed for release dates/types in migration)
			malSvc := mal.NewService(log, cfg, animeRepo, paths.MalIDPath, paths.AniDBPath)
			if _, err := malSvc.GetAnimeIDs(cmd.Context(), cacheRepo); err != nil {
				return fmt.Errorf("failed to get MAL IDs: %w", err)
			}
		}

		// Run migration - conditionally process cache-dir and rootpath
		if err := cache.MigrateCache(cmd.Context(), cacheDir, rootPath, dbPath, animeRepo, paths, log); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}

		log.Info().Msg("Migration completed successfully!")
		fmt.Printf("\n✓ Cache migration complete!\n")
		fmt.Printf("  Database: %s\n", dbPath)
		if cacheDir != "" {
			fmt.Printf("  Old cache directory (%s) can be kept for backup or removed.\n", cacheDir)
		}
		fmt.Printf("  You can now use the new cache system.\n\n")

		return nil
	},
}

func init() {
	migrateCmd.Flags().String("cache-dir", "", "directory containing HTML cache files to migrate (optional, skips MAL cache migration if not provided)")
	rootCmd.AddCommand(migrateCmd)
}
