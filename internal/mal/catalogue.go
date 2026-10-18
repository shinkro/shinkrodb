package mal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"github.com/shinkro/shinkrodb/internal/domain"
)

const malFields = "media_type,start_date,alternative_titles"

// GetAnimeIDs discovers titles through both rankings and verifies every known
// omission through the detail endpoint. Absence from a ranking is not deletion.
func (s *service) GetAnimeIDs(ctx context.Context, cacheRepo domain.CacheRepo, candidates ...domain.Anime) (int, error) {
	known := make(map[int]domain.Anime)
	archivePath := domain.AnimePath(filepath.Join(filepath.Dir(string(s.malIDPath)), string(domain.MALUnavailableFile)))
	for _, path := range []domain.AnimePath{archivePath, s.malIDPath} {
		rows, err := s.animeRepo.Get(ctx, path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, errors.Wrap(err, "failed to read existing MAL catalogue")
		}
		for _, row := range rows {
			if row.MalID <= 0 || row.MainTitle == "" {
				return 0, fmt.Errorf("invalid existing MAL entry %d", row.MalID)
			}
			known[row.MalID] = row
		}
	}
	// Community contributions can identify live entries discovery never exposed.
	// Treat these as candidates, not proof of availability; omitted IDs are still
	// verified through the detail endpoint below.
	for _, row := range candidates {
		if row.MalID <= 0 {
			return 0, errors.New("community mapping contains an invalid MAL ID")
		}
		if _, exists := known[row.MalID]; !exists {
			if row.MainTitle == "" {
				row.MainTitle = fmt.Sprintf("MAL %d", row.MalID)
			}
			known[row.MalID] = row
		}
	}
	c := *s.httpClient
	c.Transport = &clientIDTransport{ClientID: s.config.MalClientID, Transport: c.Transport}
	active := make(map[int]domain.Anime)
	for _, ranking := range []string{"bypopularity", "all"} {
		rows := []domain.Anime{}
		query := url.Values{"ranking_type": {ranking}, "limit": {"500"}, "nsfw": {"true"}, "fields": {malFields}}
		next := "https://api.myanimelist.net/v2/anime/ranking?" + query.Encode()
		visited := make(map[string]bool)
		for next != "" {
			u, err := url.Parse(next)
			if err != nil || u.Scheme != "https" || u.Host != "api.myanimelist.net" || u.Path != "/v2/anime/ranking" || u.User != nil {
				return 0, errors.New("MAL returned an invalid pagination URL")
			}
			// Keep coverage parameters even if a paging link omits them.
			q := u.Query()
			q.Set("nsfw", "true")
			q.Set("ranking_type", ranking)
			q.Set("fields", malFields)
			u.RawQuery = q.Encode()
			next = u.String()
			if visited[next] {
				return 0, errors.New("MAL pagination repeated URL")
			}
			visited[next] = true
			next, err = s.storeAnimeID(ctx, &c, next, &rows)
			if err != nil {
				return 0, errors.Wrapf(err, "failed to fetch MAL %s ranking", ranking)
			}
		}
		for _, row := range rows {
			if _, exists := active[row.MalID]; !exists {
				active[row.MalID] = row
			}
		}
		s.log.Info().Str("ranking", ranking).Int("rows", len(rows)).Int("unique_ids", len(active)).Msg("MAL discovery pass complete")
	}
	if len(active) == 0 {
		return 0, errors.New("MAL discovery returned an empty catalogue")
	}

	recovered := 0
	unavailable := []domain.Anime{}
	ids := make([]int, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if _, found := active[id]; found {
			continue
		}
		row, found, err := s.getAnimeDetail(ctx, &c, id)
		if err != nil {
			return 0, errors.Wrapf(err, "cannot verify omitted MAL %d", id)
		}
		if !found {
			unavailable = append(unavailable, known[id])
			continue
		}
		// Keep historical links if a recovered entry was never in the SQLite cache.
		previous := known[id]
		row.AnidbID, row.TvdbID, row.TmdbID = previous.AnidbID, previous.TvdbID, previous.TmdbID
		active[id] = row
		recovered++
	}
	catalogue := make([]domain.Anime, 0, len(active))
	for _, row := range active {
		catalogue = append(catalogue, row)
	}
	sort.Slice(catalogue, func(i, j int) bool { return catalogue[i].MalID < catalogue[j].MalID })
	if cacheRepo != nil {
		for _, row := range catalogue {
			if err := cacheRepo.UpsertMAL(ctx, row.MalID, fmt.Sprintf("https://myanimelist.net/anime/%d", row.MalID), row.ReleaseDate, row.Type); err != nil {
				return 0, errors.Wrapf(err, "failed to cache MAL %d", row.MalID)
			}
		}
	}
	// Keep the last metadata and mapping history for unavailable IDs; recheck them
	// on the next run so a temporary withdrawal or restored entry is recoverable.
	if err := s.animeRepo.Store(ctx, archivePath, unavailable); err != nil {
		return 0, errors.Wrap(err, "failed to store unavailable MAL IDs")
	}
	if err := s.animeRepo.Store(ctx, s.malIDPath, catalogue); err != nil {
		return 0, errors.Wrap(err, "failed to store MAL IDs")
	}
	s.log.Info().Int("live_ids", len(catalogue)).Int("recovered_omissions", recovered).Int("unavailable_ids", len(unavailable)).Msg("MAL catalogue reconciled")
	return recovered, nil
}

func (s *service) storeAnimeID(ctx context.Context, c *http.Client, requestURL string, rows *[]domain.Anime) (string, error) {
	body, err := s.getMAL(ctx, c, requestURL)
	if err != nil {
		return "", err
	}
	var response MalResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", errors.Wrap(err, "invalid MAL response")
	}
	if response.Data == nil {
		return "", errors.New("MAL response is missing its data array")
	}
	for _, item := range response.Data {
		if item.Node.ID <= 0 || item.Node.Title == "" {
			return "", errors.New("MAL response contains an invalid anime ID or title")
		}
		*rows = append(*rows, animeFromMAL(item.Node))
	}
	return response.Paging.Next, nil
}

func animeFromMAL(row malAnime) domain.Anime {
	return domain.Anime{MalID: row.ID, MainTitle: row.Title, EnglishTitle: row.AlternativeTitles.English,
		JapaneseTitle: row.AlternativeTitles.Japanese, Synonyms: row.AlternativeTitles.Synonyms,
		Type: row.MediaType, ReleaseDate: row.StartDate}
}

func (s *service) getAnimeDetail(ctx context.Context, c *http.Client, id int) (domain.Anime, bool, error) {
	requestURL := fmt.Sprintf("https://api.myanimelist.net/v2/anime/%d?nsfw=true&fields=%s", id, malFields)
	for confirmation := 0; confirmation < 2; confirmation++ {
		body, err := s.getMAL(ctx, c, requestURL)
		if errors.Is(err, errMALNotFound) {
			continue
		}
		if err != nil {
			return domain.Anime{}, false, err
		}
		var row malAnime
		if err := json.Unmarshal(body, &row); err != nil {
			return domain.Anime{}, false, err
		}
		if row.ID != id || row.Title == "" {
			return domain.Anime{}, false, errors.New("invalid MAL detail response")
		}
		return animeFromMAL(row), true, nil
	}
	return domain.Anime{}, false, nil
}

var errMALNotFound = errors.New("MAL detail not found")

// Rate limiting and bounded retries apply to both catalogue pages and details.
// Only an explicit JSON not_found response can classify a title as unavailable.
func (s *service) getMAL(ctx context.Context, c *http.Client, requestURL string) ([]byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := s.wait(ctx, 500*time.Millisecond); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(req)
		var delay time.Duration
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				err = readErr
			} else {
				if resp.StatusCode == http.StatusOK {
					return body, nil
				}
				if resp.StatusCode == http.StatusNotFound {
					var failure struct {
						Error string `json:"error"`
					}
					if json.Unmarshal(body, &failure) == nil && failure.Error == "not_found" {
						return nil, errMALNotFound
					}
				}
				err = fmt.Errorf("unexpected status code %d from MAL", resp.StatusCode)
				if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
					return nil, err
				}
				if value := resp.Header.Get("Retry-After"); value != "" {
					if seconds, parseErr := strconv.Atoi(value); parseErr == nil {
						delay = time.Duration(seconds) * time.Second
					} else if date, parseErr := http.ParseTime(value); parseErr == nil {
						delay = time.Until(date)
					}
					// Do not retry before a long provider-requested cooldown.
					if delay > 30*time.Second {
						return nil, errors.Wrap(err, "MAL requested a longer cooldown; retry the run later")
					}
				}
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == 2 {
			return nil, errors.Wrap(err, "MAL request failed after retries")
		}
		if minimum := time.Duration(1<<attempt) * time.Second; delay < minimum {
			delay = minimum
		}
		if err := s.wait(ctx, delay); err != nil {
			return nil, err
		}
	}
	panic("unreachable")
}

func waitForMAL(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func malRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.Host != "api.myanimelist.net" || req.URL.User != nil {
		return errors.New("refusing unexpected MAL API redirect")
	}
	return nil
}
