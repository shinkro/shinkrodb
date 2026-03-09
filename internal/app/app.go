package app

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/rs/zerolog"
	"github.com/varoOP/shinkrodb/internal/config"
	"github.com/varoOP/shinkrodb/internal/database"
	"github.com/varoOP/shinkrodb/internal/dedupe"
	"github.com/varoOP/shinkrodb/internal/domain"
	"github.com/varoOP/shinkrodb/internal/format"
	"github.com/varoOP/shinkrodb/internal/logger"
	"github.com/varoOP/shinkrodb/internal/mal"
	"github.com/varoOP/shinkrodb/internal/notification"
	"github.com/varoOP/shinkrodb/internal/repository"
	"github.com/varoOP/shinkrodb/internal/tmdb"
	"github.com/varoOP/shinkrodb/internal/tvdb"
)

// App represents the main application with all dependencies initialized
type App struct {
	log             zerolog.Logger
	config          *domain.Config
	paths           *domain.Paths
	animeRepo       domain.AnimeRepository
	mappingRepo     domain.MappingRepository
	malService      mal.Service
	tmdbService     tmdb.Service
	tvdbService     tvdb.Service
	dedupeService   dedupe.Service
	notificationService domain.NotificationService
}

// NewApp creates a new application instance with all dependencies initialized
func NewApp() (*App, error) {
	// Initialize logger
	log := logger.NewLogger()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize paths (will be set properly when root path is known)
	paths := domain.NewPaths(".")

	// Initialize repositories
	fileRepo := repository.NewFileRepository(log)
	var animeRepo domain.AnimeRepository = fileRepo
	var mappingRepo domain.MappingRepository = fileRepo

	// Initialize services
	malService := mal.NewService(log, cfg, animeRepo, paths.MalIDPath, paths.AniDBPath)
	tmdbService := tmdb.NewService(log, cfg, animeRepo, mappingRepo, paths)
	tvdbService := tvdb.NewService(log, animeRepo, mappingRepo, paths)
	dedupeService := dedupe.NewService(log, animeRepo)
	notificationService := notification.NewService(log, cfg.DiscordWebhookURL)

	return &App{
		log:                log,
		config:             cfg,
		paths:              paths,
		animeRepo:          animeRepo,
		mappingRepo:        mappingRepo,
		malService:         malService,
		tmdbService:        tmdbService,
		tvdbService:        tvdbService,
		dedupeService:      dedupeService,
		notificationService: notificationService,
	}, nil
}

// Run executes the full database update process
func (a *App) Run(rootPath string) (err error) {
	ctx := context.Background()
	
	// Send error notification if run fails
	defer func() {
		if err != nil {
			if notifyErr := a.notificationService.SendError(ctx, err); notifyErr != nil {
				a.log.Warn().Err(notifyErr).Msg("Failed to send error notification")
			}
		}
	}()

	// Update paths with actual root path
	a.paths = domain.NewPaths(rootPath)

	// Update services with new paths
	a.malService = mal.NewService(a.log, a.config, a.animeRepo, a.paths.MalIDPath, a.paths.AniDBPath)
	a.tmdbService = tmdb.NewService(a.log, a.config, a.animeRepo, a.mappingRepo, a.paths)
	a.tvdbService = tvdb.NewService(a.log, a.animeRepo, a.mappingRepo, a.paths)

	// Initialize database and cache repository
	// Store database in current directory (./) instead of root-path
	db, err := database.NewDB(".", a.log)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	cacheRepo := database.NewCacheRepo(a.log, db)

	// Get MAL IDs and update mal_cache
	preservedMALIDs, err := a.malService.GetAnimeIDs(ctx, cacheRepo)
	if err != nil {
		return fmt.Errorf("failed to get MAL IDs: %w", err)
	}

	// Scrape MAL for AniDB IDs (cache invalidation happens implicitly - only entries < 1 year old are used)
	if err := a.malService.ScrapeAniDBIDs(ctx, cacheRepo); err != nil {
		return fmt.Errorf("failed to scrape MAL: %w", err)
	}

	// Get TVDB IDs and update mapping
	if err := a.tvdbService.GetTvdbIDs(ctx, rootPath); err != nil {
		return fmt.Errorf("failed to get TVDB IDs: %w", err)
	}

	// Get TMDB IDs
	if err := a.tmdbService.GetTmdbIds(ctx, rootPath, cacheRepo); err != nil {
		return fmt.Errorf("failed to get TMDB IDs: %w", err)
	}

	// Check for duplicates
	animeList, err := a.animeRepo.Get(ctx, a.paths.TMDBPath)
	if err != nil {
		return fmt.Errorf("failed to get anime list: %w", err)
	}

	dupeCount, deduped, err := a.dedupeService.CheckDupes(ctx, animeList)
	if err != nil {
		return fmt.Errorf("failed to check dupes: %w", err)
	}

	a.log.Info().Int("dupe_count", dupeCount).Msg("Duplicate check complete")

	// Store deduped list
	if err := a.animeRepo.Store(ctx, a.paths.ShinkroPath, deduped); err != nil {
		return fmt.Errorf("failed to store deduped anime: %w", err)
	}

	// Calculate and log final statistics
	stats := calculateStatistics(deduped, dupeCount, preservedMALIDs)
	a.log.Info().
		Int("total_mal_ids", stats.TotalMALIDs).
		Int("preserved_mal_ids", stats.PreservedMALIDs).
		Int("mal_ids_with_anidb", stats.MALIDsWithAniDB).
		Int("mal_ids_without_anidb", stats.TotalMALIDs-stats.MALIDsWithAniDB).
		Int("total_movies", stats.TotalMovies).
		Int("movies_with_tmdb", stats.MoviesWithTMDB).
		Int("movies_without_tmdb", stats.TotalMovies-stats.MoviesWithTMDB).
		Int("total_tv_shows", stats.TotalTVShows).
		Int("tv_shows_with_tvdb", stats.TVShowsWithTVDB).
		Int("tv_shows_without_tvdb", stats.TotalTVShows-stats.TVShowsWithTVDB).
		Float64("anidb_coverage_pct", stats.AniDBCoveragePercent).
		Float64("tmdb_coverage_pct", stats.TMDBCoveragePercent).
		Float64("tvdb_coverage_pct", stats.TVDBCoveragePercent).
		Msg("=== FINAL STATISTICS ===")

	// Send success notification
	if notifyErr := a.notificationService.SendSuccess(ctx, stats); notifyErr != nil {
		a.log.Warn().Err(notifyErr).Msg("Failed to send success notification")
	}

	return nil
}

// calculateStatistics calculates comprehensive statistics from the final anime list
func calculateStatistics(animeList []domain.Anime, dupeCount, preservedMALIDs int) domain.Statistics {
	stats := domain.Statistics{
		TotalMALIDs:     len(animeList),
		DupeCount:       dupeCount,
		PreservedMALIDs: preservedMALIDs,
	}

	for _, anime := range animeList {
		// Count AniDB coverage
		if anime.AnidbID > 0 {
			stats.MALIDsWithAniDB++
		}

		// Count movies and TMDB coverage
		if anime.Type == "movie" {
			stats.TotalMovies++
			if anime.TmdbID > 0 {
				stats.MoviesWithTMDB++
			}
		}

		// Count TV shows and TVDB coverage
		if anime.Type == "tv" {
			stats.TotalTVShows++
			if anime.TvdbID > 0 {
				stats.TVShowsWithTVDB++
			}
		}
	}

	// Calculate coverage percentages
	if stats.TotalMALIDs > 0 {
		stats.AniDBCoveragePercent = (float64(stats.MALIDsWithAniDB) / float64(stats.TotalMALIDs)) * 100
	}
	if stats.TotalMovies > 0 {
		stats.TMDBCoveragePercent = (float64(stats.MoviesWithTMDB) / float64(stats.TotalMovies)) * 100
	}
	if stats.TotalTVShows > 0 {
		stats.TVDBCoveragePercent = (float64(stats.TVShowsWithTVDB) / float64(stats.TotalTVShows)) * 100
	}

	return stats
}

// GenerateMappings generates mapping files from master files
func (a *App) GenerateMappings(rootPath string) error {
	ctx := context.Background()

	// Generate TMDB mapping
	am, err := a.mappingRepo.GetTMDBMaster(ctx, filepath.Join(rootPath, "tmdb-mal-master.yaml"))
	if err != nil {
		return fmt.Errorf("failed to get TMDB master: %w", err)
	}

	// Create filtered mapping (only entries with TMDB IDs)
	filtered := &domain.AnimeMovies{}
	for _, movie := range am.AnimeMovie {
		if movie.TMDBID != 0 {
			filtered.AnimeMovie = append(filtered.AnimeMovie, movie)
		}
	}

	if err := a.mappingRepo.StoreTMDBMaster(ctx, filepath.Join(rootPath, "tmdb-mal.yaml"), filtered); err != nil {
		return fmt.Errorf("failed to create TMDB mapping: %w", err)
	}

	// Generate TVDB mapping
	tvdb, err := a.mappingRepo.GetTVDBMaster(ctx, filepath.Join(rootPath, "tvdb-mal-master.yaml"))
	if err != nil {
		return fmt.Errorf("failed to get TVDB master: %w", err)
	}

	// Create filtered mapping (only entries with TVDB IDs)
	filteredTVDB := &domain.TVDBMap{}
	for _, anime := range tvdb.Anime {
		if anime.Tvdbid != 0 {
			filteredTVDB.Anime = append(filteredTVDB.Anime, anime)
		}
	}

	if err := a.mappingRepo.StoreTVDBMaster(ctx, filepath.Join(rootPath, "tvdb-mal.yaml"), filteredTVDB); err != nil {
		return fmt.Errorf("failed to create TVDB mapping: %w", err)
	}

	return nil
}

// FormatFiles formats the YAML mapping files
func (a *App) FormatFiles(rootPath string) error {
	if err := format.FormatTMDB(rootPath, a.mappingRepo); err != nil {
		return fmt.Errorf("failed to format TMDB: %w", err)
	}

	if err := format.FormatTVDB(rootPath, a.mappingRepo); err != nil {
		return fmt.Errorf("failed to format TVDB: %w", err)
	}

	return nil
}
