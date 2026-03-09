package mal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/gocolly/colly"
	"github.com/gocolly/colly/extensions"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/varoOP/shinkrodb/internal/domain"
)

type Service interface {
	GetAnimeIDs(ctx context.Context, cacheRepo domain.CacheRepo) (int, error)
	ScrapeAniDBIDs(ctx context.Context, cacheRepo domain.CacheRepo) error
}

type service struct {
	log       zerolog.Logger
	config    *domain.Config
	animeRepo domain.AnimeRepository
	malIDPath domain.AnimePath
	anidbPath domain.AnimePath
}

type MalResponse struct {
	Data []struct {
		Node struct {
			ID          int    `json:"id"`
			Title       string `json:"title"`
			MainPicture struct {
				Medium string `json:"medium"`
				Large  string `json:"large"`
			} `json:"main_picture"`
			MediaType         string `json:"media_type"`
			AlternativeTitles struct {
				Synonyms []string `json:"synonyms"`
				English  string   `json:"en"`
				Japanese string   `json:"ja"`
			} `json:"alternative_titles"`
			StartDate string `json:"start_date"`
		} `json:"node"`
		Ranking struct {
			Rank int `json:"rank"`
		} `json:"ranking"`
	} `json:"data"`
	Paging struct {
		Next string `json:"next"`
	} `json:"paging"`
}

type clientIDTransport struct {
	Transport http.RoundTripper
	ClientID  string
}

func (c *clientIDTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.Transport == nil {
		c.Transport = http.DefaultTransport
	}
	req.Header.Add("X-MAL-CLIENT-ID", c.ClientID)
	return c.Transport.RoundTrip(req)
}

func NewService(log zerolog.Logger, config *domain.Config, animeRepo domain.AnimeRepository, malIDPath, anidbPath domain.AnimePath) Service {
	return &service{
		log:       log.With().Str("module", "mal").Logger(),
		config:    config,
		animeRepo: animeRepo,
		malIDPath: malIDPath,
		anidbPath: anidbPath,
	}
}

func (s *service) GetAnimeIDs(ctx context.Context, cacheRepo domain.CacheRepo) (int, error) {
	s.log.Info().Msg("Getting current ids from myanimelist..")
	c := &http.Client{
		Transport: &clientIDTransport{ClientID: s.config.MalClientID},
	}

	a := []domain.Anime{}
	next, err := s.storeAnimeID(ctx, c, "https://api.myanimelist.net/v2/anime/ranking?ranking_type=all&limit=500&fields={media_type,start_date,alternative_titles}", &a)
	if err != nil {
		return 0, errors.Wrap(err, "failed to fetch initial MAL IDs")
	}

	for {
		if next != "" {
			next, err = s.storeAnimeID(ctx, c, next, &a)
			if err != nil {
				return 0, errors.Wrap(err, "failed to fetch MAL IDs")
			}
		} else {
			break
		}
	}

	// Merge with existing malid.json to preserve any IDs the API may have omitted.
	// The MAL ranking API sometimes silently excludes entries, which would cause
	// previously known IDs to disappear from all downstream files.
	preserved := 0
	existing, err := s.animeRepo.Get(ctx, s.malIDPath)
	if err == nil && len(existing) > 0 {
		newIDs := make(map[int]bool, len(a))
		for _, anime := range a {
			newIDs[anime.MalID] = true
		}
		for _, anime := range existing {
			if !newIDs[anime.MalID] {
				a = append(a, anime)
				preserved++
			}
		}
		if preserved > 0 {
			s.log.Warn().Int("count", preserved).Msg("Preserved MAL IDs missing from API response")
		}
	}

	sort.SliceStable(a, func(i, j int) bool {
		return a[i].MalID < a[j].MalID
	})

	// Update mal_cache table with all MAL IDs
	if cacheRepo != nil {
		for _, anime := range a {
			url := fmt.Sprintf("https://myanimelist.net/anime/%d", anime.MalID)
			if err := cacheRepo.UpsertMAL(ctx, anime.MalID, url, anime.ReleaseDate, anime.Type); err != nil {
				s.log.Warn().Err(err).Int("mal_id", anime.MalID).Msg("failed to update MAL cache")
			}
		}
		s.log.Info().Int("count", len(a)).Msg("Updated mal_cache")
	}

	if err := s.animeRepo.Store(ctx, s.malIDPath, a); err != nil {
		return 0, errors.Wrap(err, "failed to store MAL IDs")
	}
	s.log.Info().Str("path", string(s.malIDPath)).Msg("Stored malids")

	return preserved, nil
}

func (s *service) storeAnimeID(ctx context.Context, c *http.Client, url string, a *[]domain.Anime) (string, error) {
	mal := &MalResponse{}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", errors.Wrap(err, "failed to create request")
	}

	resp, err := c.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "failed to fetch")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "failed to read response body")
	}

	err = json.Unmarshal(body, mal)
	if err != nil {
		return "", errors.Wrap(err, "failed to unmarshal response")
	}

	for _, v := range mal.Data {
		*a = append(*a, domain.Anime{
			MainTitle:     v.Node.Title,
			EnglishTitle:  v.Node.AlternativeTitles.English,
			JapaneseTitle: v.Node.AlternativeTitles.Japanese,
			Synonyms:      v.Node.AlternativeTitles.Synonyms,
			MalID:         v.Node.ID,
			Type:          v.Node.MediaType,
			ReleaseDate:   v.Node.StartDate,
		})
	}

	return mal.Paging.Next, nil
}

func (s *service) ScrapeAniDBIDs(ctx context.Context, cacheRepo domain.CacheRepo) error {
	// Get anime list from malid.json
	a, err := s.animeRepo.Get(ctx, s.malIDPath)
	if err != nil {
		return errors.Wrap(err, "failed to get anime list")
	}

	// Get all cached entries with AniDB IDs (regardless of release date)
	// Entries with AniDB IDs are always valid
	cachedMalIDs := make(map[int]bool)
	if cacheRepo != nil {
		anidbMap, err := cacheRepo.GetAniDBIDs(ctx)
		if err == nil {
			for malID, anidbID := range anidbMap {
				if anidbID > 0 {
					cachedMalIDs[malID] = true
					// Update anime list with cached AniDB ID
					for i := range a {
						if a[i].MalID == malID {
							a[i].AnidbID = anidbID
							break
						}
					}
				}
			}
		}
	}

	// Filter entries to scrape based on configured scrape mode
	toScrape := s.filterAnimeToScrape(a, cachedMalIDs)

	if len(toScrape) == 0 {
		s.log.Info().Msg("All anime already cached, skipping scrape")
		// Still store the updated list with cached AniDB IDs
		if err := s.animeRepo.Store(ctx, s.anidbPath, a); err != nil {
			return errors.Wrap(err, "failed to store AniDB IDs")
		}
		return nil
	}

	s.log.Info().Int("total", len(a)).Int("cached", len(cachedMalIDs)).Int("to_scrape", len(toScrape)).Msg("Starting scrape")

	// Build map for O(1) MAL ID lookup
	malIDToIndex := make(map[int]int, len(a))
	for i := range a {
		malIDToIndex[a[i].MalID] = i
	}

	// Use Colly for scraping (no file cache, using database cache instead)
	cc := colly.NewCollector(
		colly.AllowedDomains("myanimelist.net"),
	)

	extensions.RandomUserAgent(cc)

	r := regexp.MustCompile(`aid=(\d+)`)
	cc.OnHTML("a[href]", func(e *colly.HTMLElement) {
		if e.Attr("data-ga-click-type") == "external-links-anime-pc-anidb" {
			url := e.Attr("href")
			m := r.FindStringSubmatch(url)
			if len(m) < 2 {
				return
			}

			anidbid, err := strconv.Atoi(m[1])
			if err != nil {
				s.log.Warn().Err(err).Str("url", url).Msg("failed to parse AniDB ID")
				return
			}

			// Extract MAL ID from URL to find the right anime
			malIDMatch := regexp.MustCompile(`/anime/(\d+)`).FindStringSubmatch(e.Request.URL.String())
			if len(malIDMatch) >= 2 {
				malID, _ := strconv.Atoi(malIDMatch[1])
				// O(1) lookup using map instead of O(n) linear search
				if i, found := malIDToIndex[malID]; found {
					a[i].AnidbID = anidbid
					s.log.Debug().Int("anidbid", anidbid).Int("malid", malID).Msg("Parsed AniDB ID")

					// Update AniDB cache immediately when AniDB ID is found
					// Note: mal_cache should only be updated in GetAnimeIDs, not here
					if cacheRepo != nil {
						if err := cacheRepo.UpsertAniDB(ctx, malID, anidbid); err != nil {
							s.log.Warn().Err(err).Int("mal_id", malID).Msg("failed to update AniDB cache")
						} else {
							s.log.Debug().Int("mal_id", malID).Int("anidb_id", anidbid).Msg("Updated AniDB cache")
						}
					}
				}
			}
		}
	})

	// Since to_scrape count is expected to be low, use higher parallelism and lower delays
	cc.Limit(&colly.LimitRule{
		RandomDelay: 1 * time.Second,
		Delay:       1 * time.Second,
		Parallelism: 30,
		DomainGlob:  "*myanimelist*",
	})

	cc.OnRequest(func(r *colly.Request) {
		s.log.Debug().Str("url", r.URL.String()).Msg("visiting")
	})

	// Only scrape entries not in cache
	for _, v := range toScrape {
		cc.Visit(fmt.Sprintf("https://myanimelist.net/anime/%d", v.MalID))
	}

	// Wait for scraping to complete
	cc.Wait()

	// Note: Entries with AniDB IDs were already updated in OnHTML callback
	// Entries without AniDB IDs are not cached - they'll be scraped again next time

	if err := s.animeRepo.Store(ctx, s.anidbPath, a); err != nil {
		return errors.Wrap(err, "failed to store AniDB IDs")
	}

	return nil
}

// filterAnimeToScrape filters anime list based on configured AniDB mode
func (s *service) filterAnimeToScrape(animeList []domain.Anime, cachedMalIDs map[int]bool) []domain.Anime {
	// Skip scraping if mode is set to skip
	if s.config.AniDBMode == domain.FetchModeSkip {
		return []domain.Anime{}
	}

	toScrape := []domain.Anime{}
	currentYear := time.Now().Year()
	oneYearAgoYear := currentYear - 1

	for _, anime := range animeList {
		shouldScrape := false

		switch s.config.AniDBMode {
		case domain.FetchModeAll:
			// Scrape everything, even if already has AniDB ID in cache
			shouldScrape = true

		case domain.FetchModeMissing:
			// Scrape all entries without AniDB ID (no filters)
			// Skip if already cached with AniDB ID
			if cachedMalIDs[anime.MalID] || anime.AnidbID > 0 {
				continue
			}
			shouldScrape = true

		case domain.FetchModeDefault:
			// Default: only scrape entries that:
			// - Don't have AniDB ID
			// - Released in the last year
			// - Type = "tv"
			// Skip if already cached with AniDB ID
			if cachedMalIDs[anime.MalID] || anime.AnidbID > 0 {
				continue
			}

			if anime.Type != "tv" {
				shouldScrape = false
				break
			}

			if anime.ReleaseDate != "" {
				// Extract year from release_date (format: "YYYY-MM-DD", "YYYY-MM", or "YYYY")
				if len(anime.ReleaseDate) >= 4 {
					releaseYearStr := anime.ReleaseDate[:4]
					if releaseYear, parseErr := strconv.Atoi(releaseYearStr); parseErr == nil {
						shouldScrape = releaseYear >= oneYearAgoYear
					}
				}
			} else {
				// If no release date, don't scrape (too old or unknown)
				shouldScrape = false
			}
		}

		if shouldScrape {
			toScrape = append(toScrape, anime)
		}
	}

	return toScrape
}
