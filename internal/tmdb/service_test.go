package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
	"github.com/shinkro/shinkrodb/internal/repository"
	"github.com/shinkro/shinkrodb/internal/testutil"
)

func TestMovieMasterPreservesCommunityMappings(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := repository.NewFileRepository(zerolog.Nop())
	path := filepath.Join(root, "tmdb-mal-master.yaml")
	existing := &domain.AnimeMovies{AnimeMovie: []domain.AnimeMovie{
		{MALID: 1, TMDBID: 100, MainTitle: "Corrected movie"},
		{MALID: 3, TMDBID: 300, MainTitle: "Manual movie"},
		{MALID: 4, TMDBID: 400, MainTitle: "Omitted movie"},
		{MALID: 5, TMDBID: 500, MainTitle: "Changed type"},
	}}
	if err := repo.StoreTMDBMaster(ctx, path, existing); err != nil {
		t.Fatal(err)
	}
	paths := domain.NewPaths(root)
	anime := []domain.Anime{
		{MalID: 1, Type: "movie", MainTitle: "Updated title", TmdbID: 101},
		{MalID: 2, Type: "movie", MainTitle: "Automatic movie", TmdbID: 200},
		{MalID: 3, Type: "movie", MainTitle: "Manual movie"},
		{MalID: 5, Type: "unknown", MainTitle: "Changed type"},
		{MalID: 6, Type: "movie", MainTitle: "Missing movie"},
		{MalID: 2, Type: "movie", MainTitle: "Automatic movie", TmdbID: 200},
	}
	if err := repo.Store(ctx, paths.TVDBPath, anime); err != nil {
		t.Fatal(err)
	}
	s := NewService(zerolog.Nop(), &domain.Config{TMDBMode: domain.FetchModeSkip}, repo, repo, paths)
	if err := s.GetTmdbIds(ctx, root, nil); err != nil {
		t.Fatal(err)
	}
	master, err := repo.GetTMDBMaster(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.AnimeMovie{
		{MALID: 1, TMDBID: 100, MainTitle: "Updated title"},
		{MALID: 2, MainTitle: "Automatic movie"},
		{MALID: 3, TMDBID: 300, MainTitle: "Manual movie"},
		{MALID: 4, TMDBID: 400, MainTitle: "Omitted movie"},
		{MALID: 5, TMDBID: 500, MainTitle: "Changed type"},
		{MALID: 6, MainTitle: "Missing movie"},
	}
	if !reflect.DeepEqual(master.AnimeMovie, want) {
		t.Fatalf("master = %#v; want %#v", master.AnimeMovie, want)
	}
	unmapped, err := repo.GetTMDBMaster(ctx, filepath.Join(root, "tmdb-mal-unmapped.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unmapped.AnimeMovie, []domain.AnimeMovie{want[5]}) {
		t.Fatalf("unmapped = %#v", unmapped)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GetTmdbIds(ctx, root, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("regeneration changed the master")
	}
}

func TestInvalidMovieMasterIsNotOverwritten(t *testing.T) {
	for _, contents := range []string{"animeMovies: [", "typo: []", "animeMovies:\n  - {malid: 1, mainTitle: X, tmdbid: 10}\n  - {malid: 1, mainTitle: X, tmdbid: 11}\n"} {
		t.Run(contents, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "tmdb-mal-master.yaml")
			unmapped := filepath.Join(root, "tmdb-mal-unmapped.yaml")
			if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unmapped, []byte("previous output"), 0644); err != nil {
				t.Fatal(err)
			}
			repo := repository.NewFileRepository(zerolog.Nop())
			s := NewService(zerolog.Nop(), &domain.Config{}, repo, repo, domain.NewPaths(root)).(*service)
			if err := s.updateMasterFiles(context.Background(), root, []domain.Anime{{MalID: 2, Type: "movie"}}); err == nil {
				t.Fatal("expected an error")
			}
			for p, want := range map[string]string{path: contents, unmapped: "previous output"} {
				got, err := os.ReadFile(p)
				if err != nil || string(got) != want {
					t.Fatalf("file overwritten: %s: %s (%v)", p, got, err)
				}
			}
		})
	}
}

func TestMatchMovie(t *testing.T) {
	for _, tt := range []struct {
		name, date, results string
		pages, want         int
	}{
		{"main title and date", "2000-01-01", `[{"id":10,"title":"Example","release_date":"2000-01-01"}]`, 1, 10},
		{"English title and whitespace", "2000-01-01", `[{"id":10,"title":" english   TITLE ","release_date":"2000-01-01"}]`, 1, 10},
		{"original title", "2000-01-01", `[{"id":10,"title":"Other","original_title":"Example","release_date":"2000-01-01"}]`, 1, 10},
		{"single result wrong date", "2000-01-01", `[{"id":10,"title":"Example","release_date":"2001-01-01"}]`, 1, 0},
		{"same date wrong title", "2000-01-01", `[{"id":10,"title":"Different","release_date":"2000-01-01"}]`, 1, 0},
		{"partial date", "2000", `[{"id":10,"title":"Example","release_date":"2000"}]`, 1, 0},
		{"invalid date", "2000-02-30", `[{"id":10,"title":"Example","release_date":"2000-02-30"}]`, 1, 0},
		{"ambiguous match", "2000-01-01", `[{"id":10,"title":"Example","release_date":"2000-01-01"},{"id":11,"title":"Example","release_date":"2000-01-01"}]`, 1, 0},
		{"incomplete search pages", "2000-01-01", `[{"id":10,"title":"Example","release_date":"2000-01-01"}]`, 2, 0},
		{"invalid ID", "2000-01-01", `[{"id":0,"title":"Example","release_date":"2000-01-01"}]`, 1, 0},
		{"no results", "2000-01-01", `[]`, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var response TMDBAPIResponse
			if err := json.Unmarshal([]byte(`{"total_results":1,"results":`+tt.results+`}`), &response); err != nil {
				t.Fatal(err)
			}
			response.TotalPages = tt.pages
			got := matchMovie(domain.Anime{MainTitle: "Example", EnglishTitle: "English Title", ReleaseDate: tt.date}, &response)
			if got != tt.want {
				t.Fatalf("match = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTMDBFailureDoesNotPublishOutput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile("anime-list.xml", []byte("<anime-list/>"), 0644); err != nil {
		t.Fatal(err)
	}
	paths := domain.NewPaths(root)
	repo := repository.NewFileRepository(zerolog.Nop())
	if err := repo.Store(ctx, paths.TVDBPath, []domain.Anime{{MalID: 1, Type: "movie", MainTitle: "Example", ReleaseDate: "2000-01-01"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(string(paths.TMDBPath), []byte("previous output"), 0644); err != nil {
		t.Fatal(err)
	}
	s := NewService(zerolog.Nop(), &domain.Config{TMDBMode: domain.FetchModeMissing}, repo, repo, paths).(*service)
	s.httpClient.Transport = testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) { return testutil.Response(503, "unavailable"), nil })
	if err := s.GetTmdbIds(ctx, root, nil); err == nil {
		t.Fatal("expected upstream error")
	}
	got, err := os.ReadFile(string(paths.TMDBPath))
	if err != nil || string(got) != "previous output" {
		t.Fatalf("output changed: %s (%v)", got, err)
	}
}

func TestMovieFetchModesRespectExistingLinks(t *testing.T) {
	anime := []domain.Anime{{MalID: 1, Type: "movie", TmdbID: 10}, {MalID: 2, Type: "movie"}, {MalID: 3, Type: "tv"}}
	for _, tt := range []struct {
		mode domain.FetchMode
		want []int
	}{
		{domain.FetchModeDefault, []int{2}}, {domain.FetchModeMissing, []int{2}},
		{domain.FetchModeAll, []int{1, 2}}, {domain.FetchModeSkip, nil},
	} {
		t.Run(string(tt.mode), func(t *testing.T) {
			s := &service{config: &domain.Config{TMDBMode: tt.mode}}
			var ids []int
			for _, row := range s.filterMoviesToFetch(anime) {
				ids = append(ids, row.MalID)
			}
			if !reflect.DeepEqual(ids, tt.want) {
				t.Fatalf("fetch IDs = %v, want %v", ids, tt.want)
			}
		})
	}
}

func TestTMDBRequestErrorsDoNotExposeAPIKey(t *testing.T) {
	s := NewService(zerolog.Nop(), &domain.Config{}, nil, nil, nil).(*service)
	s.httpClient.Transport = testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, context.Canceled })
	_, err := s.searchTMDB(context.Background(), "https://api.themoviedb.org/3/search/movie?api_key=test-secret")
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("unsafe request error: %v", err)
	}
}
