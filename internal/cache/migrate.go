package cache

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/database"
	"github.com/shinkro/shinkrodb/internal/domain"
	"golang.org/x/net/html"
)

// MigrateCache migrates existing Colly HTML cache to SQLite database
// This is a temporary function that can be removed after migration is complete
// - cacheDir: if provided, migrates HTML cache files (MAL and AniDB IDs)
// - rootPath: if provided, updates TMDB IDs from existing JSON file
// - animeRepo and paths are needed to get release date, type, and TMDB data
func MigrateCache(ctx context.Context, cacheDir, rootPath, dbPath string, animeRepo domain.AnimeRepository, paths *domain.Paths, log zerolog.Logger) error {
	log.Info().
		Str("cache_dir", cacheDir).
		Str("root_path", rootPath).
		Str("db_path", dbPath).
		Msg("Starting cache migration")

	// Get anime data from MAL API results (malid.json) which includes release dates and types
	// This is more reliable than AniDB path since all entries have MAL data
	// Only needed if cacheDir is provided (for HTML cache migration)
	var animeMap map[int]domain.Anime
	if cacheDir != "" {
		animeList, err := animeRepo.Get(ctx, paths.MalIDPath)
		if err != nil {
			// If anime data doesn't exist, we can still migrate but without release date/type
			log.Warn().Err(err).Msg("Failed to get anime data, migrating without release date/type")
			animeList = []domain.Anime{}
		}

		// Build map for quick lookup
		animeMap = make(map[int]domain.Anime)
		for _, a := range animeList {
			animeMap[a.MalID] = a
		}
	}

	// Open database (schema migration happens automatically)
	dbDir := filepath.Dir(dbPath)
	db, err := database.NewDB(dbDir, log)
	if err != nil {
		return errors.Wrap(err, "failed to open database")
	}
	defer db.Close()

	// Create repository for database operations
	cacheRepo := database.NewCacheRepo(log, db)

	// Migrate HTML cache if cacheDir is provided
	if cacheDir != "" {
		// Prepare regex patterns
		malIDRegex := regexp.MustCompile(`<link\s*rel="canonical"\s*\n*href="https://myanimelist\.net/anime/(\d+)/`)
		anidbIDRegex := regexp.MustCompile(`https://anidb\.net/perl-bin/animedb\.pl\?show=anime&amp.aid=(\d+)`)

		// Walk cache directory
		var migrated, skipped, errorCount int
		err := filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				log.Warn().Err(err).Str("path", path).Msg("error accessing file")
				errorCount++
				return nil // Continue processing
			}

			// Skip directories
			if info.IsDir() {
				return nil
			}

			// Read and parse HTML file (normalizes HTML structure)
			file, err := os.Open(path)
			if err != nil {
				log.Warn().Err(err).Str("path", path).Msg("failed to open file")
				errorCount++
				return nil
			}

			// Parse HTML to normalize structure
			doc, err := html.Parse(file)
			file.Close()
			if err != nil {
				log.Warn().Err(err).Str("path", path).Msg("failed to parse HTML")
				errorCount++
				return nil
			}

			// Render parsed HTML back to string (normalized)
			var b bytes.Buffer
			if err := html.Render(&b, doc); err != nil {
				log.Warn().Err(err).Str("path", path).Msg("failed to render HTML")
				errorCount++
				return nil
			}

			htmlContent := b.String()

			// Extract MAL ID from HTML
			// log.Debug().Str("htmlContent", htmlContent).Msg("htmlContent")
			malIDMatch := malIDRegex.FindStringSubmatch(htmlContent)
			log.Debug().Strs("malIDMatch", malIDMatch).Msg("malIDMatch")
			if len(malIDMatch) < 2 {
				skipped++
				return nil // Skip files without MAL ID
			}

			malID, err := strconv.Atoi(malIDMatch[1])
			if err != nil {
				log.Warn().Err(err).Str("path", path).Msg("failed to parse MAL ID")
				skipped++
				return nil
			}

			// Extract AniDB ID from HTML
			// Look for AniDB link in the HTML - should be in format: href="...aid=12345..."
			anidbID := 0
			anidbMatch := anidbIDRegex.FindStringSubmatch(htmlContent)
			log.Debug().Strs("anidbMatch", anidbMatch).Msg("anidbMatch")
			matchedString := "no match"
			if len(anidbMatch) >= 2 {
				matchedString = anidbMatch[0]
				parsedAnidbID, parseErr := strconv.Atoi(anidbMatch[1])
				if parseErr == nil && parsedAnidbID > 0 {
					anidbID = parsedAnidbID
				}
			}

			// Debug: Log if mal_id equals anidb_id (should not happen)
			if malID == anidbID && anidbID > 0 {
				log.Warn().
					Int("mal_id", malID).
					Int("anidb_id", anidbID).
					Str("path", path).
					Str("matched_string", matchedString).
					Msg("WARNING: mal_id equals anidb_id during migration - this should not happen!")
			}

			// Insert into database using new separate tables
			// Note: mal_cache should only be updated in GetAnimeIDs, not here
			// Update AniDB cache only
			if err := cacheRepo.UpsertAniDB(ctx, malID, anidbID); err != nil {
				log.Warn().Err(err).Int("mal_id", malID).Str("path", path).Msg("failed to insert AniDB cache")
				errorCount++
				return nil
			}

			migrated++
			if migrated%100 == 0 {
				log.Info().Int("migrated", migrated).Msg("Migration progress")
			}

			return nil
		})

		if err != nil {
			return errors.Wrap(err, "failed to walk cache directory")
		}

		log.Info().
			Int("migrated", migrated).
			Int("skipped", skipped).
			Int("errors", errorCount).
			Msg("HTML cache migration complete")
	}

	// Update TMDB IDs from existing JSON file if rootPath is provided
	if rootPath != "" {
		log.Info().Str("tmdb_path", string(paths.TMDBPath)).Msg("Updating TMDB IDs from existing JSON file")

		// Get anime data with TMDB IDs from the JSON file
		animeList, err := animeRepo.Get(ctx, paths.TMDBPath)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to get TMDB data from JSON file, skipping TMDB ID update")
		} else {
			// Build map of MAL ID to anime data (for TMDB ID, release date, and type)
			animeMap := make(map[int]domain.Anime)
			for _, a := range animeList {
				if a.TmdbID > 0 {
					animeMap[a.MalID] = a
				}
			}

			// Update or create cache entries with TMDB IDs
			// Note: mal_cache should only be updated in GetAnimeIDs, not here
			updatedCount := 0
			for malID, anime := range animeMap {
				// UpsertTMDB will update existing entries or create new ones if they don't exist
				if err := cacheRepo.UpsertTMDB(ctx, malID, anime.TmdbID); err != nil {
					log.Warn().Err(err).Int("mal_id", malID).Int("tmdb_id", anime.TmdbID).Msg("failed to update/create TMDB ID")
					continue
				}
				updatedCount++
			}

			log.Info().
				Int("updated_or_created", updatedCount).
				Int("total_tmdb_ids", len(animeMap)).
				Msg("TMDB ID update/creation complete")
		}
	}

	log.Info().Msg("Cache migration completed successfully")
	return nil
}
