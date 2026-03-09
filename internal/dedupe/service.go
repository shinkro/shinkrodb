package dedupe

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
)

type Service interface {
	CheckDupes(ctx context.Context, anime []domain.Anime) (int, []domain.Anime, error)
}

type service struct {
	log         zerolog.Logger
	animeRepo   domain.AnimeRepository
	aidTitleMap map[int]string
}

func NewService(log zerolog.Logger, animeRepo domain.AnimeRepository) Service {
	return &service{
		log:         log.With().Str("module", "dedupe").Logger(),
		animeRepo:   animeRepo,
		aidTitleMap: make(map[int]string),
	}
}

func (s *service) CheckDupes(ctx context.Context, anime []domain.Anime) (int, []domain.Anime, error) {
	// Build map: AniDB ID -> []indices for O(1) duplicate detection
	// Only consider entries with AniDB ID and type "tv"
	anidbToIndices := make(map[int][]int)
	for i, a := range anime {
		if a.AnidbID > 0 && a.Type == "tv" {
			anidbToIndices[a.AnidbID] = append(anidbToIndices[a.AnidbID], i)
		}
	}

	// Find AniDB IDs with duplicates (more than one entry)
	duplicateAnidbIDs := make(map[int]bool)
	for anidbID, indices := range anidbToIndices {
		if len(indices) > 1 {
			duplicateAnidbIDs[anidbID] = true
			// Initialize title map entry
			if _, ok := s.aidTitleMap[anidbID]; !ok {
				s.aidTitleMap[anidbID] = ""
			}
		}
	}

	if len(duplicateAnidbIDs) == 0 {
		return 0, anime, nil
	}

	// Fill title map for all duplicate AniDB IDs
	if err := s.fillAidTitleMap(ctx); err != nil {
		s.log.Warn().Err(err).Msg("failed to fill AniDB title map")
		// Continue with empty titles - will keep all entries
	}

	// Build set of indices to remove (more efficient than recursive calls)
	indicesToRemove := make(map[int]bool)

	// Check each duplicate group
	for anidbID := range duplicateAnidbIDs {
		mainTitle := s.aidTitleMap[anidbID]
		if mainTitle == "" {
			// No title found, can't determine which to remove - keep all
			continue
		}

		// Find entries that don't match the AniDB main title
		for _, index := range anidbToIndices[anidbID] {
			if !strings.EqualFold(anime[index].MainTitle, mainTitle) {
				indicesToRemove[index] = true
				s.log.Debug().
					Int("mal_id", anime[index].MalID).
					Str("anime_title", anime[index].MainTitle).
					Str("anidb_title", mainTitle).
					Int("anidb_id", anidbID).
					Msg("Marking non-matching entry for removal")
			}
		}
	}

	if len(indicesToRemove) == 0 {
		return len(duplicateAnidbIDs), anime, nil
	}

	s.log.Info().
		Int("dupe_groups", len(duplicateAnidbIDs)).
		Int("entries_to_remove", len(indicesToRemove)).
		Msg("Found duplicates")

	// Remove entries in reverse order to maintain correct indices
	deduped := make([]domain.Anime, 0, len(anime)-len(indicesToRemove))
	for i := range anime {
		if !indicesToRemove[i] {
			deduped = append(deduped, anime[i])
		}
	}

	return len(duplicateAnidbIDs), deduped, nil
}


func (s *service) fillAidTitleMap(ctx context.Context) error {
	anidb := &Animetitles{}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/Anime-Lists/anime-lists/raw/master/animetitles.xml", nil)
	if err != nil {
		return errors.Wrap(err, "failed to create request")
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return errors.Wrap(err, "failed to fetch animetitles.xml")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	xr := xml.NewDecoder(resp.Body)
	err = xr.Decode(anidb)
	if err != nil {
		return errors.Wrap(err, "failed to decode XML")
	}

	// Build a map of AniDB ID -> main title for O(1) lookup
	anidbTitleMap := make(map[int]string)
	for _, anime := range anidb.Anime {
		aid, err := strconv.Atoi(anime.Aid)
		if err != nil {
			continue // Skip invalid AniDB IDs
		}

		// Find main title
		for _, title := range anime.Title {
			if title.Type == "main" {
				anidbTitleMap[aid] = title.Text
				break
			}
		}
	}

	// Fill requested AniDB IDs from the map
	for key := range s.aidTitleMap {
		if s.aidTitleMap[key] != "" {
			continue // Already filled
		}
		if title, found := anidbTitleMap[key]; found {
			s.aidTitleMap[key] = title
		}
	}

	return nil
}

type Animetitles struct {
	XMLName xml.Name `xml:"animetitles"`
	Anime   []struct {
		Aid   string `xml:"aid,attr"`
		Title []struct {
			Text string `xml:",chardata"`
			Type string `xml:"type,attr"`
			Lang string `xml:"lang,attr"`
		} `xml:"title"`
	} `xml:"anime"`
}
