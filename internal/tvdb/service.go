package tvdb

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
	"github.com/shinkro/shinkrodb/pkg/animelist"
)

type Service interface {
	GetTvdbIDs(ctx context.Context, rootPath string) error
}

type service struct {
	log        zerolog.Logger
	animeRepo  domain.AnimeRepository
	mappingRepo domain.MappingRepository
	paths      *domain.Paths
}

func NewService(log zerolog.Logger, animeRepo domain.AnimeRepository, mappingRepo domain.MappingRepository, paths *domain.Paths) Service {
	return &service{
		log:        log.With().Str("module", "tvdb").Logger(),
		animeRepo:  animeRepo,
		mappingRepo: mappingRepo,
		paths:      paths,
	}
}

func (s *service) GetTvdbIDs(ctx context.Context, rootPath string) error {
	// Store anime-list.xml in current directory (./) instead of root-path
	al, err := animelist.NewAnimeList(ctx, ".")
	if err != nil {
		return errors.Wrap(err, "failed to create anime list")
	}

	a, err := s.animeRepo.Get(ctx, s.paths.AniDBPath)
	if err != nil {
		return errors.Wrap(err, "failed to get anime list")
	}

	updated := 0
	for i, anime := range a {
		if anime.Type == "tv" && anime.AnidbID > 0 {
			if tvdbid := al.GetTvdbID(anime.AnidbID); tvdbid > 0 {
				a[i].TvdbID = tvdbid
				updated++
			}
		}
	}

	if err := s.animeRepo.Store(ctx, s.paths.TVDBPath, a); err != nil {
		return errors.Wrap(err, "failed to store TVDB IDs")
	}

	s.log.Info().Int("updated_count", updated).Msg("TVDB ID mapping complete")

	// Create and update TVDB mapping master (similar to TMDB)
	if err := s.createAndUpdateMaster(ctx, rootPath); err != nil {
		return errors.Wrap(err, "failed to create and update TVDB mapping")
	}

	return nil
}

func (s *service) createAndUpdateMaster(ctx context.Context, rootPath string) error {
	// Get anime data for creating unmapped map
	animeList, err := s.animeRepo.Get(ctx, s.paths.MalIDPath)
	if err != nil {
		return errors.Wrap(err, "failed to get anime list for mapping")
	}

	// Create unmapped TVDB map from anime data
	unmapped := &domain.TVDBMap{}
	for _, anime := range animeList {
		unmapped.Anime = append(unmapped.Anime, domain.TVDBAnime{
			Malid:        anime.MalID,
			Title:        anime.MainTitle,
			Type:         anime.Type,
			Tvdbid:       0,
			TvdbSeason:   0,
			Start:        0,
			UseMapping:   false,
			AnimeMapping: []domain.AnimeMapping{},
		})
	}

	// Store unmapped file
	unmappedPath := filepath.Join(rootPath, "tvdb-mal-unmapped.yaml")
	if err := s.mappingRepo.StoreTVDBMaster(ctx, unmappedPath, unmapped); err != nil {
		return errors.Wrap(err, "failed to store unmapped map")
	}

	// Update master file
	masterPath := filepath.Join(rootPath, "tvdb-mal-master.yaml")
	master, err := s.mappingRepo.GetTVDBMaster(ctx, masterPath)
	if err != nil {
		// If master file doesn't exist, create it from unmapped (first run)
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file or directory") || strings.Contains(err.Error(), "file does not exist") {
			return s.mappingRepo.StoreTVDBMaster(ctx, masterPath, unmapped)
		}
		return errors.Wrap(err, "failed to get TVDB master")
	}

	// Merge master data into unmapped (preserve existing mappings)
	masterMap := make(map[int]domain.TVDBAnime)
	for _, v := range master.Anime {
		if v.Tvdbid != 0 {
			masterMap[v.Malid] = v
		}
	}

	for i, v := range unmapped.Anime {
		if masterAnime, ok := masterMap[v.Malid]; ok {
			unmapped.Anime[i].AnimeMapping = masterAnime.AnimeMapping
			unmapped.Anime[i].Start = masterAnime.Start
			unmapped.Anime[i].TvdbSeason = masterAnime.TvdbSeason
			unmapped.Anime[i].Tvdbid = masterAnime.Tvdbid
			unmapped.Anime[i].UseMapping = masterAnime.UseMapping
		}
	}

	// Store updated master
	if err := s.mappingRepo.StoreTVDBMaster(ctx, masterPath, unmapped); err != nil {
		return errors.Wrap(err, "failed to store TVDB master")
	}

	s.log.Info().Msg("TVDB mapping master updated")
	return nil
}

