package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

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
	httpClient  *http.Client
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
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *service) GetTmdbIds(ctx context.Context, rootPath string, cacheRepo domain.CacheRepo) error {
	a, err := s.animeRepo.Get(ctx, s.paths.TVDBPath)
	if err != nil {
		return errors.Wrap(err, "failed to get anime list")
	}

	// Get cached TMDB IDs
	if cacheRepo != nil {
		tmdbMap, err := cacheRepo.GetTMDBIDs(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to read TMDB cache")
		}
		for i := range a {
			if tmdbID := tmdbMap[a[i].MalID]; tmdbID > 0 {
				a[i].TmdbID = tmdbID
			}
		}
	}

	// Filter movies to fetch based on configured TMDB mode
	toFetch := s.filterMoviesToFetch(a)

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
		if err := ctx.Err(); err != nil {
			return err
		}
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
							return errors.Wrapf(err, "failed to cache TMDB ID for MAL %d", anime.MalID)
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

			if _, err := time.Parse("2006-01-02", anime.ReleaseDate); err != nil {
				noTmdbTotal++
				s.log.Debug().Str("title", anime.MainTitle).Msg("does not have a complete release date")
				continue
			}

			year := s.getYear(anime.ReleaseDate)
			query.Add("year", year)
			target.RawQuery = query.Encode()

			tmdb, err := s.searchTMDB(ctx, target.String())
			if err != nil {
				return errors.Wrapf(err, "failed to search TMDB for MAL %d", anime.MalID)
			}

			tmdbID := matchMovie(anime, tmdb)
			matched = tmdbID > 0

			// Update anime list and cache if matched
			if matched && tmdbID > 0 {
				// O(1) lookup using map
				if i, found := malIDToIndex[anime.MalID]; found {
					a[i].TmdbID = tmdbID
					withTmdbTotal++

					// Update TMDB cache immediately when TMDB ID is found
					if cacheRepo != nil {
						if err := cacheRepo.UpsertTMDB(ctx, anime.MalID, tmdbID); err != nil {
							return errors.Wrapf(err, "failed to cache TMDB ID for MAL %d", anime.MalID)
						} else {
							s.log.Debug().Int("mal_id", anime.MalID).Int("tmdb_id", tmdbID).Msg("Updated TMDB cache")
						}
					}
				}
			}

			if !matched {
				noTmdbTotal++
				s.log.Warn().
					Str("title", anime.MainTitle).
					Int("mal_id", anime.MalID).
					Str("english_title", anime.EnglishTitle).
					Str("release_date", anime.ReleaseDate).
					Msg("No TMDB ID found")
			}
		}
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
func (s *service) filterMoviesToFetch(animeList []domain.Anime) []domain.Anime {
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
			if anime.TmdbID > 0 {
				continue
			}
			shouldFetch = true

		case domain.FetchModeDefault:
			// Default: only fetch movies without TMDB ID
			// Skip if already cached with TMDB ID
			if anime.TmdbID > 0 {
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
	masterPath := filepath.Join(rootPath, "tmdb-mal-master.yaml")
	existing, err := s.mappingRepo.GetTMDBMaster(ctx, masterPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errors.Wrap(err, "failed to read TMDB master")
		}
		existing = &domain.AnimeMovies{}
	}

	byMAL := make(map[int]domain.AnimeMovie)
	for _, row := range existing.AnimeMovie {
		if previous, ok := byMAL[row.MALID]; ok && previous.TMDBID != row.TMDBID {
			return fmt.Errorf("conflicting TMDB mappings for MAL %d", row.MALID)
		}
		byMAL[row.MALID] = row
	}
	// The master includes every movie, not just automatic lookup failures.
	// Keep imported IDs in JSON; a nonzero master ID remains a community decision.
	for _, anime := range animeList {
		if anime.Type != "movie" {
			continue
		}
		row := byMAL[anime.MalID]
		row.MALID = anime.MalID
		row.MainTitle = anime.MainTitle
		byMAL[anime.MalID] = row
	}
	master := &domain.AnimeMovies{}
	for _, row := range byMAL {
		master.AnimeMovie = append(master.AnimeMovie, row)
	}
	sort.Slice(master.AnimeMovie, func(i, j int) bool { return master.AnimeMovie[i].MALID < master.AnimeMovie[j].MALID })
	unmapped := &domain.AnimeMovies{}
	seen := make(map[int]bool)
	for _, anime := range animeList {
		if anime.Type == "movie" && anime.TmdbID == 0 && byMAL[anime.MalID].TMDBID == 0 && !seen[anime.MalID] {
			unmapped.Add(anime.MainTitle, 0, anime.MalID)
			seen[anime.MalID] = true
		}
	}
	if err := s.mappingRepo.StoreTMDBMaster(ctx, masterPath, master); err != nil {
		return errors.Wrap(err, "failed to store TMDB master")
	}
	if err := s.mappingRepo.StoreTMDBMaster(ctx, filepath.Join(rootPath, "tmdb-mal-unmapped.yaml"), unmapped); err != nil {
		return errors.Wrap(err, "failed to store unmapped movies")
	}

	return nil
}

func (s *service) searchTMDB(ctx context.Context, requestURL string) (*TMDBAPIResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create request")
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		// url.Error includes the query string, which contains the API key.
		if requestErr, ok := err.(*url.Error); ok {
			err = requestErr.Err
		}
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

// matchMovie requires a unique title-and-date match. A search returning one
// result, or a shared release date alone, does not establish movie identity.
func matchMovie(anime domain.Anime, response *TMDBAPIResponse) int {
	if _, err := time.Parse("2006-01-02", anime.ReleaseDate); err != nil || response.TotalPages > 1 {
		return 0
	}
	titles := append([]string{anime.MainTitle, anime.EnglishTitle, anime.JapaneseTitle}, anime.Synonyms...)
	matchedID := 0
	for _, result := range response.Results {
		if result.ID <= 0 || result.ReleaseDate != anime.ReleaseDate {
			continue
		}
		for _, title := range titles {
			normalized := strings.Join(strings.Fields(title), " ")
			if normalized == "" || (!strings.EqualFold(normalized, strings.Join(strings.Fields(result.Title), " ")) &&
				!strings.EqualFold(normalized, strings.Join(strings.Fields(result.OriginalTitle), " "))) {
				continue
			}
			if matchedID != 0 && matchedID != result.ID {
				return 0
			}
			matchedID = result.ID
			break
		}
	}
	return matchedID
}
