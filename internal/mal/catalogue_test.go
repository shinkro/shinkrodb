package mal

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
	"github.com/shinkro/shinkrodb/internal/repository"
	"github.com/shinkro/shinkrodb/internal/testutil"
)

func TestCatalogueReconcilesLiveMissingUnavailableAndRestoredIDs(t *testing.T) {
	ctx := context.Background()
	paths := domain.NewPaths(t.TempDir())
	repo := repository.NewFileRepository(zerolog.Nop())
	old := []domain.Anime{{MalID: 7, MainTitle: "Withdrawn"}, {MalID: 8, MainTitle: "Omitted", AnidbID: 80}}
	if err := repo.Store(ctx, paths.MalIDPath, old); err != nil {
		t.Fatal(err)
	}
	if err := repo.Store(ctx, paths.MALUnavailablePath, []domain.Anime{{MalID: 9, MainTitle: "Restored"}}); err != nil {
		t.Fatal(err)
	}
	s := NewService(zerolog.Nop(), &domain.Config{MalClientID: "test"}, repo, paths.MalIDPath, paths.AniDBPath).(*service)
	s.wait = func(context.Context, time.Duration) error { return nil }
	checks := make(map[string]int)
	restored := false
	s.httpClient.Transport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		checks[req.URL.Path]++
		if req.URL.Path == "/v2/anime/ranking" {
			q := req.URL.Query()
			if q.Get("nsfw") != "true" || q.Get("fields") != malFields {
				t.Fatalf("coverage parameters lost: %s", req.URL)
			}
			if q.Get("ranking_type") == "bypopularity" {
				return testutil.Response(200, `{"data":[{"node":{"id":10,"title":"Unranked"}}]}`), nil
			}
			return testutil.Response(200, `{"data":[{"node":{"id":20,"title":"Ranked"}}]}`), nil
		}
		switch req.URL.Path {
		case "/v2/anime/7":
			if !restored {
				return testutil.Response(404, `{"error":"not_found"}`), nil
			}
			return testutil.Response(200, `{"id":7,"title":"Restored withdrawal"}`), nil
		case "/v2/anime/8":
			return testutil.Response(200, `{"id":8,"title":"Updated omitted title"}`), nil
		case "/v2/anime/9":
			return testutil.Response(200, `{"id":9,"title":"Restored"}`), nil
		case "/v2/anime/99":
			return testutil.Response(200, `{"id":99,"title":"Unlisted contribution"}`), nil
		}
		return nil, fmt.Errorf("unexpected request: %s", req.URL)
	})
	recovered, err := s.GetAnimeIDs(ctx, nil, domain.Anime{MalID: 99, MainTitle: "Contributor candidate"})
	if err != nil {
		t.Fatal(err)
	}
	live, err := repo.Get(ctx, paths.MalIDPath)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := repo.Get(ctx, paths.MALUnavailablePath)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int{}
	for _, row := range live {
		ids = append(ids, row.MalID)
	}
	if recovered != 3 || !reflect.DeepEqual(ids, []int{8, 9, 10, 20, 99}) || len(archive) != 1 || archive[0].MalID != 7 || live[0].AnidbID != 80 || checks["/v2/anime/7"] != 2 {
		t.Fatalf("recovered=%d live=%#v archive=%#v checks=%v", recovered, live, archive, checks)
	}
	restored = true
	if _, err := s.GetAnimeIDs(ctx, nil); err != nil {
		t.Fatal(err)
	}
	archive, err = repo.Get(ctx, paths.MALUnavailablePath)
	if err != nil || len(archive) != 0 {
		t.Fatalf("restored ID stayed unavailable: %#v %v", archive, err)
	}
}

func TestOmittedEntryRequiresConfirmedNotFound(t *testing.T) {
	for _, kind := range []string{"404 then 200", "404 then 503", "403", "429", "HTML 404", "malformed 200", "wrong ID", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			paths := domain.NewPaths(t.TempDir())
			repo := repository.NewFileRepository(zerolog.Nop())
			old := []domain.Anime{{MalID: 7, MainTitle: "Keep me"}}
			if err := repo.Store(ctx, paths.MalIDPath, old); err != nil {
				t.Fatal(err)
			}
			s := NewService(zerolog.Nop(), &domain.Config{}, repo, paths.MalIDPath, paths.AniDBPath).(*service)
			s.wait = func(context.Context, time.Duration) error { return nil }
			calls := 0
			s.httpClient.Transport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/v2/anime/ranking" {
					return testutil.Response(200, `{"data":[{"node":{"id":1,"title":"Live"}}]}`), nil
				}
				calls++
				if strings.HasPrefix(kind, "404 then") && calls == 1 {
					return testutil.Response(404, `{"error":"not_found"}`), nil
				}
				switch kind {
				case "404 then 200":
					return testutil.Response(200, `{"id":7,"title":"Keep me"}`), nil
				case "404 then 503":
					return testutil.Response(503, `{"error":"unavailable"}`), nil
				case "403":
					return testutil.Response(403, `{"error":"forbidden"}`), nil
				case "429":
					return testutil.Response(429, `{"error":"too_many_requests"}`), nil
				case "HTML 404":
					return testutil.Response(404, "<html>Not found</html>"), nil
				case "malformed 200":
					return testutil.Response(200, `{broken`), nil
				case "wrong ID":
					return testutil.Response(200, `{"id":8,"title":"Wrong entry"}`), nil
				default:
					return nil, context.DeadlineExceeded
				}
			})
			_, err := s.GetAnimeIDs(ctx, nil)
			if (err == nil) != (kind == "404 then 200") {
				t.Fatalf("unexpected result: %v", err)
			}
			live, readErr := repo.Get(ctx, paths.MalIDPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if err != nil && !reflect.DeepEqual(live, old) {
				t.Fatalf("failed verification changed catalogue: %#v", live)
			}
		})
	}
}

func TestMALRetriesAndHonorsRetryAfter(t *testing.T) {
	s := NewService(zerolog.Nop(), &domain.Config{}, nil, "", "").(*service)
	delays := []time.Duration{}
	s.wait = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	calls := 0
	s.httpClient.Transport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			r := testutil.Response(429, "{}")
			r.Header.Set("Retry-After", "4")
			return r, nil
		}
		return testutil.Response(200, `{"id":1}`), nil
	})
	if _, err := s.getMAL(context.Background(), s.httpClient, "https://api.myanimelist.net/v2/anime/1"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !reflect.DeepEqual(delays, []time.Duration{500 * time.Millisecond, 4 * time.Second, 500 * time.Millisecond}) {
		t.Fatalf("calls=%d delays=%v", calls, delays)
	}
}

func TestMALRejectsForeignPaginationAndRestoresCoverageParameters(t *testing.T) {
	for _, next := range []string{"https://attacker.invalid/steal", "https://api.myanimelist.net/v2/anime/ranking?offset=500"} {
		t.Run(next, func(t *testing.T) {
			paths := domain.NewPaths(t.TempDir())
			repo := repository.NewFileRepository(zerolog.Nop())
			s := NewService(zerolog.Nop(), &domain.Config{}, repo, paths.MalIDPath, paths.AniDBPath).(*service)
			s.wait = func(context.Context, time.Duration) error { return nil }
			s.httpClient.Transport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "api.myanimelist.net" {
					t.Fatal("client ID sent to foreign host")
				}
				if req.URL.Query().Get("nsfw") != "true" {
					t.Fatal("lost nsfw parameter")
				}
				if req.URL.Query().Get("offset") != "" {
					return testutil.Response(200, `{"data":[]}`), nil
				}
				return testutil.Response(200, fmt.Sprintf(`{"data":[{"node":{"id":1,"title":"Live"}}],"paging":{"next":%q}}`, next)), nil
			})
			_, err := s.GetAnimeIDs(context.Background(), nil)
			if (err != nil) != strings.Contains(next, "attacker") {
				t.Fatalf("unexpected result %v", err)
			}
		})
	}
}
