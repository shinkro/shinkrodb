package mal

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/database"
	"github.com/shinkro/shinkrodb/internal/domain"
	"github.com/shinkro/shinkrodb/internal/repository"
	"github.com/shinkro/shinkrodb/internal/testutil"
)

func TestCataloguePreservesOmissionsAndDeduplicatesPagination(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths := domain.NewPaths(root)
	repo := repository.NewFileRepository(zerolog.Nop())
	if err := repo.Store(ctx, paths.MalIDPath, []domain.Anime{{MalID: 7, MainTitle: "Omitted"}, {MalID: 7, MainTitle: "Omitted"}}); err != nil {
		t.Fatal(err)
	}
	s := NewService(zerolog.Nop(), &domain.Config{MalClientID: "test"}, repo, paths.MalIDPath, paths.AniDBPath).(*service)
	s.wait = func(context.Context, time.Duration) error { return nil }
	requests := 0
	s.httpClient.Transport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Header.Get("X-MAL-CLIENT-ID") != "test" {
			t.Fatal("missing client ID")
		}
		if req.URL.Path == "/v2/anime/7" {
			return testutil.Response(200, `{"id":7,"title":"Omitted"}`), nil
		}
		if req.URL.Query().Get("page") == "2" {
			return testutil.Response(200, `{"data":[{"node":{"id":1,"title":"Repeated"}},{"node":{"id":2,"title":"New"}}]}`), nil
		}
		return testutil.Response(200, `{"data":[{"node":{"id":1,"title":"First"}}],"paging":{"next":"https://api.myanimelist.net/v2/anime/ranking?page=2"}}`), nil
	})
	preserved, err := s.GetAnimeIDs(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, paths.MalIDPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.Anime{{MalID: 1, MainTitle: "First"}, {MalID: 2, MainTitle: "New"}, {MalID: 7, MainTitle: "Omitted"}}
	if requests != 5 || preserved != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("requests=%d preserved=%d catalogue=%#v", requests, preserved, got)
	}
}

func TestFailedCatalogueImportPreservesPreviousFile(t *testing.T) {
	for _, kind := range []string{"corrupt existing catalogue", "failed pagination", "cyclic pagination", "failed cache write", "invalid response"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			paths := domain.NewPaths(t.TempDir())
			repo := repository.NewFileRepository(zerolog.Nop())
			if err := repo.Store(ctx, paths.MalIDPath, []domain.Anime{{MalID: 7, MainTitle: "Previous"}}); err != nil {
				t.Fatal(err)
			}
			if kind == "corrupt existing catalogue" {
				if err := os.WriteFile(string(paths.MalIDPath), []byte("{broken"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(string(paths.MalIDPath))
			if err != nil {
				t.Fatal(err)
			}
			s := NewService(zerolog.Nop(), &domain.Config{}, repo, paths.MalIDPath, paths.AniDBPath).(*service)
			s.wait = func(context.Context, time.Duration) error { return nil }
			requests := 0
			s.httpClient.Transport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.URL.Path == "/v2/anime/7" {
					return testutil.Response(200, `{"id":7,"title":"Previous"}`), nil
				}
				if requests > 10 {
					return testutil.Response(503, "pagination did not terminate"), nil
				}
				if req.URL.Query().Get("page") == "2" {
					if kind == "cyclic pagination" {
						return testutil.Response(200, `{"data":[],"paging":{"next":"https://api.myanimelist.net/v2/anime/ranking?page=2"}}`), nil
					}
					return testutil.Response(503, "unavailable"), nil
				}
				body := `{"data":[{"node":{"id":1,"title":"New"}}]}`
				if kind == "failed pagination" || kind == "cyclic pagination" {
					body = `{"data":[{"node":{"id":1,"title":"New"}}],"paging":{"next":"https://api.myanimelist.net/v2/anime/ranking?page=2"}}`
				}
				if kind == "invalid response" {
					body = `{"unexpected":"response"}`
				}
				return testutil.Response(200, body), nil
			})
			var cache domain.CacheRepo
			if kind == "failed cache write" {
				db, err := database.NewDB(t.TempDir(), zerolog.Nop())
				if err != nil {
					t.Fatal(err)
				}
				cache = database.NewCacheRepo(zerolog.Nop(), db)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.GetAnimeIDs(ctx, cache)
			if err == nil {
				t.Fatal("expected import failure")
			}
			if kind == "cyclic pagination" && !strings.Contains(err.Error(), "pagination repeated") {
				t.Fatal(err)
			}
			after, err := os.ReadFile(string(paths.MalIDPath))
			if err != nil || string(before) != string(after) {
				t.Fatalf("catalogue changed: %s (%v)", after, err)
			}
		})
	}
}

func TestFailedScrapeDoesNotPublishPartialCatalogue(t *testing.T) {
	ctx := context.Background()
	paths := domain.NewPaths(t.TempDir())
	repo := repository.NewFileRepository(zerolog.Nop())
	if err := repo.Store(ctx, paths.MalIDPath, []domain.Anime{{MalID: 1, Type: "tv"}, {MalID: 2, Type: "tv"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(string(paths.AniDBPath), []byte("previous output"), 0644); err != nil {
		t.Fatal(err)
	}
	s := NewService(zerolog.Nop(), &domain.Config{AniDBMode: domain.FetchModeMissing}, repo, paths.MalIDPath, paths.AniDBPath).(*service)
	s.httpClient.Transport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/anime/1" {
			return testutil.Response(200, `<a data-ga-click-type="external-links-anime-pc-anidb" href="https://anidb.net/?aid=10">AniDB</a>`), nil
		}
		return testutil.Response(503, "unavailable"), nil
	})
	if err := s.ScrapeAniDBIDs(ctx, nil); err == nil {
		t.Fatal("expected scrape failure")
	}
	got, err := os.ReadFile(string(paths.AniDBPath))
	if err != nil || string(got) != "previous output" {
		t.Fatalf("output changed: %s (%v)", got, err)
	}
}

func TestScrapeCacheReadFailureDoesNotEraseIDs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths := domain.NewPaths(root)
	repo := repository.NewFileRepository(zerolog.Nop())
	if err := repo.Store(ctx, paths.MalIDPath, []domain.Anime{{MalID: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(string(paths.AniDBPath), []byte("previous output"), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := database.NewDB(filepath.Join(root, "shinkrodb"), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	cache := database.NewCacheRepo(zerolog.Nop(), db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s := NewService(zerolog.Nop(), &domain.Config{AniDBMode: domain.FetchModeSkip}, repo, paths.MalIDPath, paths.AniDBPath)
	if err := s.ScrapeAniDBIDs(ctx, cache); err == nil {
		t.Fatal("expected cache error")
	}
	got, err := os.ReadFile(string(paths.AniDBPath))
	if err != nil || string(got) != "previous output" {
		t.Fatalf("output changed: %s (%v)", got, err)
	}
}
