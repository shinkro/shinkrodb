package config

import (
	"fmt"

	"github.com/spf13/viper"
	"github.com/shinkro/shinkrodb/internal/domain"
)

// Load loads configuration from multiple sources:
// 1. Config file (config.toml, optional)
// 2. Environment variables (SHINKRODB_*)
func Load() (*domain.Config, error) {
	cfg := &domain.Config{}

	// Load from Viper (config file + env vars)
	cfg.MalClientID = viper.GetString("mal_client_id")
	cfg.TmdbApiKey = viper.GetString("tmdb_api_key")
	cfg.DiscordWebhookURL = viper.GetString("discord_webhook_url")
	
	// AniDB mode (default: "default")
	anidbModeStr := viper.GetString("anidb_mode")
	if anidbModeStr == "" {
		cfg.AniDBMode = domain.FetchModeDefault
	} else {
		cfg.AniDBMode = domain.FetchMode(anidbModeStr)
		// Validate AniDB mode
		if cfg.AniDBMode != domain.FetchModeDefault && 
		   cfg.AniDBMode != domain.FetchModeMissing && 
		   cfg.AniDBMode != domain.FetchModeAll &&
		   cfg.AniDBMode != domain.FetchModeSkip {
			return nil, fmt.Errorf("invalid anidb_mode: %s (must be 'default', 'missing', 'all', or 'skip')", anidbModeStr)
		}
	}

	// TMDB mode (default: "default")
	tmdbModeStr := viper.GetString("tmdb_mode")
	if tmdbModeStr == "" {
		cfg.TMDBMode = domain.FetchModeDefault
	} else {
		cfg.TMDBMode = domain.FetchMode(tmdbModeStr)
		// Validate TMDB mode
		if cfg.TMDBMode != domain.FetchModeDefault && 
		   cfg.TMDBMode != domain.FetchModeMissing && 
		   cfg.TMDBMode != domain.FetchModeAll &&
		   cfg.TMDBMode != domain.FetchModeSkip {
			return nil, fmt.Errorf("invalid tmdb_mode: %s (must be 'default', 'missing', 'all', or 'skip')", tmdbModeStr)
		}
	}

	// Validate required fields
	if cfg.MalClientID == "" {
		return nil, fmt.Errorf("mal_client_id is required (set via config.toml or SHINKRODB_MAL_CLIENT_ID environment variable)")
	}
	if cfg.TmdbApiKey == "" {
		return nil, fmt.Errorf("tmdb_api_key is required (set via config.toml or SHINKRODB_TMDB_API_KEY environment variable)")
	}

	return cfg, nil
}


// NewConfig is kept for backward compatibility but is deprecated
// Use Load() instead
func NewConfig() *domain.Config {
	cfg, err := Load()
	if err != nil {
		// For backward compatibility, we'll try to continue
		// but this should be migrated to proper error handling
		return &domain.Config{}
	}
	return cfg
}
