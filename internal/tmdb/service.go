package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
	"github.com/shinkro/shinkrodb/pkg/animelist"
)

type Service interface {
	GetTmdbIds(ctx context.Context, rootPath string, cacheRepo domain.CacheRepo) error
}

type service struct {
	log         zerolog.Logger
	config      *domain.Config
	animeRepo   domain.AnimeRepository
	mappingRepo domain.MappingRepository
	paths       *domain.Paths
}

type TMDBAPIResponse struct {
	Page    int `json:"page"`
	Results []struct {
		Adult            bool    `json:"adult"`
		BackdropPath     string  `json:"backdrop_path"`
		GenreIds         []int   `json:"genre_ids"`
		ID               int     `json:"id"`
		OriginalLanguage string  `json:"original_language"`
		OriginalTitle    string  `json:"original_title"`
		Overview         string  `json:"overview"`
		Popularity       float64 `json:"popularity"`
		PosterPath       string  `json:"poster_path"`
		ReleaseDate      string  `json:"release_date"`
		Title            string  `json:"title"`
		Video            bool    `json:"video"`
		VoteAverage      float64 `json:"vote_average"`
		VoteCount        int     `json:"vote_count"`
	} `json:"results"`
	TotalPages   int `json:"total_pages"`
	TotalResults int `json:"total_results"`
}

func NewService(log zerolog.Logger, config *domain.Config, animeRepo domain.AnimeRepository, mappingRepo domain.MappingRepository, paths *domain.Paths) Service {
	return &service{
		log:         log.With().Str("module", "tmdb").Logger(),
		config:      config,
		animeRepo:   animeRepo,
		mappingRepo: mappingRepo,
		paths:       paths,
	}
}

func (s *service) GetTmdbIds(ctx context.Context, rootPath string, cacheRepo domain.CacheRepo) error {
	a, err := s.animeRepo.Get(ctx, s.paths.TVDBPath)
	if err != nil {
		return errors.Wrap(err, "failed to get anime list")
	}

	// Get cached TMDB IDs
	cachedTmdbIDs := make(map[int]int)
	if cacheRepo != nil {
		tmdbMap, err := cacheRepo.GetTMDBIDs(ctx)
		if err == nil {
			cachedTmdbIDs = tmdbMap
			// Update anime list with cached TMDB IDs
			for i := range a {
				if tmdbID, found := cachedTmdbIDs[a[i].MalID]; found && tmdbID > 0 {
					a[i].TmdbID = tmdbID
				}
			}
		}
	}

	// Filter movies to fetch based on configured TMDB mode
	toFetch := s.filterMoviesToFetch(a, cachedTmdbIDs)

	if len(toFetch) == 0 {
		s.log.Info().Msg("All movies already cached, skipping TMDB lookups")
		// Still store the updated list with cached TMDB IDs
		if err := s.animeRepo.Store(ctx, s.paths.TMDBPath, a); err != nil {
			return errors.Wrap(err, "failed to store TMDB IDs")
		}
		// Still update master files
		return s.updateMasterFiles(ctx, rootPath, a)
	}

	// Load anime-list.xml from current directory (./) instead of root-path
	al, err := animelist.NewAnimeList(ctx, ".")
	if err != nil {
		s.log.Warn().Err(err).Msg("failed to load anime-list.xml, will use TMDB API only")
		al = nil
	}

	u := s.buildUrl(s.config.TmdbApiKey)
	am := &domain.AnimeMovies{}
	noTmdbTotal := 0
	withTmdbTotal := 0
	fromAnimeListTotal := 0
	totalMovies := 0

	// Build map for O(1) MAL ID lookup
	malIDToIndex := make(map[int]int, len(a))
	for i := range a {
		malIDToIndex[a[i].MalID] = i
	}

	for _, anime := range toFetch {
		totalMovies++
		matched := false

		// First, try to get TMDB ID from anime-list.xml if we have an AniDB ID
		if al != nil && anime.AnidbID > 0 {
			if tmdbID := al.GetTmdbID(anime.AnidbID); tmdbID > 0 {
				// Found in anime-list.xml
				if i, found := malIDToIndex[anime.MalID]; found {
					a[i].TmdbID = tmdbID
					withTmdbTotal++
					fromAnimeListTotal++
					matched = true
					s.log.Debug().
						Str("title", anime.MainTitle).
						Int("tmdb_id", tmdbID).
						Int("anidb_id", anime.AnidbID).
						Msg("TMDBID found in anime-list.xml")

					// Update TMDB cache immediately when TMDB ID is found
					if cacheRepo != nil {
						if err := cacheRepo.UpsertTMDB(ctx, anime.MalID, tmdbID); err != nil {
							s.log.Warn().Err(err).Int("mal_id", anime.MalID).Msg("failed to update TMDB cache")
						} else {
							s.log.Debug().Int("mal_id", anime.MalID).Int("tmdb_id", tmdbID).Msg("Updated TMDB cache")
						}
					}
				}
			}
		}

		// If not found in anime-list.xml, fall back to TMDB API
		if !matched {
			target := *u
			query := target.Query()
			if anime.EnglishTitle != "" {
				query.Add("query", anime.EnglishTitle)
			} else {
				query.Add("query", anime.MainTitle)
			}

			if anime.ReleaseDate == "" {
				noTmdbTotal++
				s.log.Debug().Str("title", anime.MainTitle).Msg("does not have a release date")
				am.Add(anime.MainTitle, 0, anime.MalID)
				continue
			}

			year := s.getYear(anime.ReleaseDate)
			query.Add("year", year)
			target.RawQuery = query.Encode()

			tmdb, err := s.searchTMDB(ctx, target.String())
			if err != nil {
				s.log.Warn().Err(err).Str("title", anime.MainTitle).Msg("failed to search TMDB")
				noTmdbTotal++
				am.Add(anime.MainTitle, 0, anime.MalID)
				continue
			}

			// Match on exact date OR single result
			var tmdbID int
			for _, result := range tmdb.Results {
				if result.ReleaseDate == anime.ReleaseDate || tmdb.TotalResults == 1 {
					tmdbID = result.ID
					matched = true
					s.log.Debug().Str("title", anime.MainTitle).Int("tmdb_id", result.ID).Msg("TMDBID added from API")
					break
				} else {
					// Log warning for each non-matching result
					s.log.Warn().
						Str("title", anime.MainTitle).
						Str("tmdb_date", result.ReleaseDate).
						Str("mal_date", anime.ReleaseDate).
						Int("total_results", tmdb.TotalResults).
						Msg("TMDB date does not match MAL date and has multiple results")
				}
			}

			// Update anime list and cache if matched
			if matched && tmdbID > 0 {
				// O(1) lookup using map
				if i, found := malIDToIndex[anime.MalID]; found {
					a[i].TmdbID = tmdbID
					withTmdbTotal++

					// Update TMDB cache immediately when TMDB ID is found
					if cacheRepo != nil {
						if err := cacheRepo.UpsertTMDB(ctx, anime.MalID, tmdbID); err != nil {
							s.log.Warn().Err(err).Int("mal_id", anime.MalID).Msg("failed to update TMDB cache")
						} else {
							s.log.Debug().Int("mal_id", anime.MalID).Int("tmdb_id", tmdbID).Msg("Updated TMDB cache")
						}
					}
				}
			}

			if !matched {
				noTmdbTotal++
				am.Add(anime.MainTitle, 0, anime.MalID)
				s.log.Warn().
					Str("title", anime.MainTitle).
					Int("mal_id", anime.MalID).
					Str("english_title", anime.EnglishTitle).
					Str("release_date", anime.ReleaseDate).
					Msg("No TMDB ID found")
			}
		}
	}

	if err := s.animeRepo.Store(ctx, s.paths.TMDBPath, a); err != nil {
		return errors.Wrap(err, "failed to store TMDB IDs")
	}

	s.log.Info().
		Int("total_movies", totalMovies).
		Int("with_tmdbid", withTmdbTotal).
		Int("from_anime_list", fromAnimeListTotal).
		Int("from_api", withTmdbTotal-fromAnimeListTotal).
		Int("without_tmdbid", noTmdbTotal).
		Msg("TMDB ID mapping complete")

	if err := s.animeRepo.Store(ctx, s.paths.TMDBPath, a); err != nil {
		return errors.Wrap(err, "failed to store TMDB IDs")
	}

	return s.updateMasterFiles(ctx, rootPath, a)
}

// filterMoviesToFetch filters movies based on configured TMDB mode
func (s *service) filterMoviesToFetch(animeList []domain.Anime, cachedTmdbIDs map[int]int) []domain.Anime {
	// Skip fetching if mode is set to skip
	if s.config.TMDBMode == domain.FetchModeSkip {
		return []domain.Anime{}
	}

	toFetch := []domain.Anime{}

	for _, anime := range animeList {
		if anime.Type != "movie" {
			continue
		}

		shouldFetch := false

		switch s.config.TMDBMode {
		case domain.FetchModeAll:
			// Fetch everything, even if already has TMDB ID in cache
			shouldFetch = true

		case domain.FetchModeMissing:
			// Fetch all movies without TMDB ID (no filters)
			// Skip if already cached with TMDB ID
			if _, found := cachedTmdbIDs[anime.MalID]; found && anime.TmdbID > 0 {
				continue
			}
			shouldFetch = true

		case domain.FetchModeDefault:
			// Default: only fetch movies without TMDB ID
			// Skip if already cached with TMDB ID
			if _, found := cachedTmdbIDs[anime.MalID]; found && anime.TmdbID > 0 {
				continue
			}
			shouldFetch = true
		}

		if shouldFetch {
			toFetch = append(toFetch, anime)
		}
	}

	return toFetch
}

// updateMasterFiles updates the TMDB master mapping files
func (s *service) updateMasterFiles(ctx context.Context, rootPath string, animeList []domain.Anime) error {
	am := &domain.AnimeMovies{}
	for _, anime := range animeList {
		if anime.Type == "movie" && anime.TmdbID == 0 {
			am.Add(anime.MainTitle, 0, anime.MalID)
		}
	}

	if err := s.mappingRepo.StoreTMDBMaster(ctx, filepath.Join(rootPath, "tmdb-mal-unmapped.yaml"), am); err != nil {
		return errors.Wrap(err, "failed to store unmapped movies")
	}

	// Update master
	existingMaster, err := s.mappingRepo.GetTMDBMaster(ctx, filepath.Join(rootPath, "tmdb-mal-master.yaml"))
	if err != nil {
		// File doesn't exist yet, create new one
		existingMaster = &domain.AnimeMovies{}
	}

	if err := s.updateMaster(ctx, existingMaster, am, filepath.Join(rootPath, "tmdb-mal-master.yaml")); err != nil {
		return errors.Wrap(err, "failed to update master")
	}

	return nil
}

func (s *service) searchTMDB(ctx context.Context, url string) (*TMDBAPIResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create request")
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read response")
	}

	tmdb := &TMDBAPIResponse{}
	err = json.Unmarshal(body, tmdb)
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal response")
	}

	return tmdb, nil
}

func (s *service) buildUrl(apikey string) *url.URL {
	baseUrl := "https://api.themoviedb.org/3/search/movie"
	u, err := url.Parse(baseUrl)
	if err != nil {
		log.Fatal(err)
	}

	query := u.Query()
	query.Add("api_key", apikey)
	query.Add("language", "en-US")
	query.Add("page", "1")
	query.Add("include_adult", "true")
	u.RawQuery = query.Encode()
	return u
}

func (s *service) getYear(d string) string {
	r := regexp.MustCompile(`^\d{4,4}`)
	return r.FindString(d)
}

func (s *service) updateMaster(ctx context.Context, existing, new *domain.AnimeMovies, path string) error {
	malidToTmdbid := map[int]int{}
	if existing != nil {
		for i := range existing.AnimeMovie {
			if existing.AnimeMovie[i].TMDBID != 0 {
				malidToTmdbid[existing.AnimeMovie[i].MALID] = existing.AnimeMovie[i].TMDBID
			}
		}
	}

	for ii := range new.AnimeMovie {
		if tmdbid, found := malidToTmdbid[new.AnimeMovie[ii].MALID]; found {
			new.AnimeMovie[ii].TMDBID = tmdbid
		}
	}

	return s.mappingRepo.StoreTMDBMaster(ctx, path, new)
}
