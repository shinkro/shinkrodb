package mal

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gocolly/colly"
	"github.com/gocolly/colly/extensions"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
)

type Service interface {
	GetAnimeIDs(ctx context.Context, cacheRepo domain.CacheRepo, candidates ...domain.Anime) (int, error)
	ScrapeAniDBIDs(ctx context.Context, cacheRepo domain.CacheRepo) error
}

type service struct {
	log        zerolog.Logger
	config     *domain.Config
	animeRepo  domain.AnimeRepository
	malIDPath  domain.AnimePath
	anidbPath  domain.AnimePath
	httpClient *http.Client
	wait       func(context.Context, time.Duration) error
}

type malAnime struct {
	ID                int    `json:"id"`
	Title             string `json:"title"`
	MediaType         string `json:"media_type"`
	AlternativeTitles struct {
		Synonyms []string `json:"synonyms"`
		English  string   `json:"en"`
		Japanese string   `json:"ja"`
	} `json:"alternative_titles"`
	StartDate string `json:"start_date"`
}

type MalResponse struct {
	Data []struct {
		Node malAnime `json:"node"`
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
	req.Header.Set("X-MAL-CLIENT-ID", c.ClientID)
	return c.Transport.RoundTrip(req)
}

func NewService(log zerolog.Logger, config *domain.Config, animeRepo domain.AnimeRepository, malIDPath, anidbPath domain.AnimePath) Service {
	return &service{
		log:        log.With().Str("module", "mal").Logger(),
		config:     config,
		animeRepo:  animeRepo,
		malIDPath:  malIDPath,
		anidbPath:  anidbPath,
		httpClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: malRedirect},
		wait:       waitForMAL,
	}
}

func (s *service) ScrapeAniDBIDs(ctx context.Context, cacheRepo domain.CacheRepo) error {
	// Get anime list from malid.json
	a, err := s.animeRepo.Get(ctx, s.malIDPath)
	if err != nil {
		return errors.Wrap(err, "failed to get anime list")
	}

	// Reuse positive cached links; --anidb=all still explicitly refreshes them.
	cachedMalIDs := make(map[int]bool)
	if cacheRepo != nil {
		anidbMap, err := cacheRepo.GetAniDBIDs(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to read AniDB cache")
		}
		for i := range a {
			if anidbID := anidbMap[a[i].MalID]; anidbID > 0 {
				cachedMalIDs[a[i].MalID] = true
				a[i].AnidbID = anidbID
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
	cc.WithTransport(contextTransport{ctx: ctx, base: s.httpClient.Transport})
	cc.SetRequestTimeout(s.httpClient.Timeout)
	var cacheErr error

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
							cacheErr = errors.Wrapf(err, "failed to cache AniDB ID for MAL %d", malID)
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := cc.Visit(fmt.Sprintf("https://myanimelist.net/anime/%d", v.MalID)); err != nil {
			return errors.Wrapf(err, "failed to scrape MAL %d", v.MalID)
		}
		if cacheErr != nil {
			return cacheErr
		}
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

// Colly does not attach our caller's context to requests itself.
type contextTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t contextTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req.WithContext(t.ctx))
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
