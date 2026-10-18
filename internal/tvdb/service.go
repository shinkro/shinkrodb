package tvdb

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
	"github.com/shinkro/shinkrodb/pkg/animelist"
)

type Service interface {
	GetTvdbIDs(ctx context.Context, rootPath string) error
}

type service struct {
	log         zerolog.Logger
	animeRepo   domain.AnimeRepository
	mappingRepo domain.MappingRepository
	paths       *domain.Paths
}

func NewService(log zerolog.Logger, animeRepo domain.AnimeRepository, mappingRepo domain.MappingRepository, paths *domain.Paths) Service {
	return &service{
		log:         log.With().Str("module", "tvdb").Logger(),
		animeRepo:   animeRepo,
		mappingRepo: mappingRepo,
		paths:       paths,
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

	masterPath := filepath.Join(rootPath, "tvdb-mal-master.yaml")
	master, err := s.mappingRepo.GetTVDBMaster(ctx, masterPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errors.Wrap(err, "failed to get TVDB master")
		}
		master = &domain.TVDBMap{}
	}

	byMAL := make(map[int]domain.TVDBAnime)
	active := make(map[int]bool, len(animeList))
	for _, row := range master.Anime {
		if previous, ok := byMAL[row.Malid]; ok && !reflect.DeepEqual(previous, row) {
			return errors.Errorf("conflicting TVDB mappings for MAL %d", row.Malid)
		}
		byMAL[row.Malid] = row
	}
	for _, anime := range animeList {
		active[anime.MalID] = true
		row, exists := byMAL[anime.MalID]
		if !exists {
			row = domain.TVDBAnime{Malid: anime.MalID, AnimeMapping: []domain.AnimeMapping{}}
		}
		row.Title = anime.MainTitle
		row.Type = anime.Type
		byMAL[anime.MalID] = row
	}
	// Preserve mappings and in-progress rules even if an import omits an ID.
	master = &domain.TVDBMap{}
	for _, row := range byMAL {
		master.Anime = append(master.Anime, row)
	}
	sort.Slice(master.Anime, func(i, j int) bool { return master.Anime[i].Malid < master.Anime[j].Malid })
	unmapped := &domain.TVDBMap{}
	for _, row := range master.Anime {
		if row.Tvdbid == 0 && active[row.Malid] {
			unmapped.Anime = append(unmapped.Anime, row)
		}
	}
	if err := s.mappingRepo.StoreTVDBMaster(ctx, masterPath, master); err != nil {
		return errors.Wrap(err, "failed to store TVDB master")
	}
	if err := s.mappingRepo.StoreTVDBMaster(ctx, filepath.Join(rootPath, "tvdb-mal-unmapped.yaml"), unmapped); err != nil {
		return errors.Wrap(err, "failed to store unmapped map")
	}

	s.log.Info().Msg("TVDB mapping master updated")
	return nil
}
