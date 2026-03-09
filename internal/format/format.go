package format

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinkro/shinkrodb/internal/domain"
)

func FormatTMDB(rootPath string, mappingRepo domain.MappingRepository) error {
	ctx := context.Background()
	tmdbPath := filepath.Join(rootPath, "tmdb-mal-master.yaml")
	
	tmdb, err := mappingRepo.GetTMDBMaster(ctx, tmdbPath)
	if err != nil {
		// Skip formatting if file doesn't exist
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file or directory") || strings.Contains(err.Error(), "file does not exist") {
			return nil
		}
		return fmt.Errorf("failed to get TMDB master: %w", err)
	}

	if err := mappingRepo.StoreTMDBMaster(ctx, tmdbPath, tmdb); err != nil {
		return fmt.Errorf("failed to store TMDB master: %w", err)
	}
	return nil
}

func FormatTVDB(rootPath string, mappingRepo domain.MappingRepository) error {
	ctx := context.Background()
	tvdbPath := filepath.Join(rootPath, "tvdb-mal-master.yaml")
	
	tvdb, err := mappingRepo.GetTVDBMaster(ctx, tvdbPath)
	if err != nil {
		// Skip formatting if file doesn't exist
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file or directory") || strings.Contains(err.Error(), "file does not exist") {
			return nil
		}
		return fmt.Errorf("failed to get TVDB master: %w", err)
	}

	if err := mappingRepo.StoreTVDBMaster(ctx, tvdbPath, tvdb); err != nil {
		return fmt.Errorf("failed to store TVDB master: %w", err)
	}
	return nil
}
