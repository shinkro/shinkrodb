package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/database"
	"github.com/shinkro/shinkrodb/internal/dedupe"
	"github.com/shinkro/shinkrodb/internal/domain"
	"github.com/shinkro/shinkrodb/internal/notification"
	"github.com/shinkro/shinkrodb/internal/repository"
	"github.com/shinkro/shinkrodb/internal/testutil"
)

// Exercise a complete batch with cached links and local upstream fixtures. No
// credentials or provider network calls are needed, including with --tmdb=skip.
func TestOfflineRunPreservesCommunityCorrectionsAndCanonicalAniDBLookup(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	ctx := context.Background()
	log := zerolog.Nop()
	repo := repository.NewFileRepository(log)
	if err := os.WriteFile("anime-list.xml", []byte(`<anime-list><anime anidbid="2400" tvdbid="78916"/></anime-list>`), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := database.NewDB(root, log)
	if err != nil {
		t.Fatal(err)
	}
	cache := database.NewCacheRepo(log, db)
	for _, id := range []int{384, 395} {
		if err := cache.UpsertAniDB(ctx, id, 2400); err != nil {
			t.Fatal(err)
		}
	}
	if err := cache.UpsertTMDB(ctx, 1, 101); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := repo.StoreTMDBMaster(ctx, filepath.Join(root, "tmdb-mal-master.yaml"), &domain.AnimeMovies{
		AnimeMovie: []domain.AnimeMovie{{MALID: 1, TMDBID: 100, MainTitle: "Corrected movie"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.StoreTVDBMaster(ctx, filepath.Join(root, "tvdb-mal-master.yaml"), &domain.TVDBMap{
		Anime: []domain.TVDBAnime{{Malid: 384, Tvdbid: 333, Title: "Gantz", Type: "tv", AnimeMapping: []domain.AnimeMapping{}}},
	}); err != nil {
		t.Fatal(err)
	}
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	http.DefaultTransport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "github.com" && req.URL.Path == "/Anime-Lists/anime-lists/raw/master/animetitles.xml" {
			return testutil.Response(200, `<animetitles><anime aid="2400"><title type="main">Gantz</title></anime></animetitles>`), nil
		}
		if req.URL.Host != "api.myanimelist.net" {
			return nil, fmt.Errorf("unexpected network request: %s", req.URL.Host)
		}
		return testutil.Response(200, `{"data":[{"node":{"id":1,"title":"Movie","media_type":"movie"}},{"node":{"id":384,"title":"Gantz","media_type":"tv"}},{"node":{"id":395,"title":"Gantz 2nd Stage","media_type":"tv"}}]}`), nil
	})
	a := &App{log: log, config: &domain.Config{MalClientID: "test", AniDBMode: domain.FetchModeSkip, TMDBMode: domain.FetchModeSkip},
		animeRepo: repo, mappingRepo: repo, dedupeService: dedupe.NewService(log, repo), notificationService: notification.NewService(log, "")}
	if err := a.Run(root); err != nil {
		t.Fatal(err)
	}
	anime, err := repo.Get(ctx, domain.NewPaths(root).ShinkroPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(anime) != 2 || anime[1].MalID != 384 || anime[1].AnidbID != 2400 {
		t.Fatalf("AniDB 2400 must resolve unambiguously to season 1 (MAL 384): %#v", anime)
	}
	complete, err := repo.Get(ctx, domain.NewPaths(root).TMDBPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(complete) != 3 || complete[2].MalID != 395 {
		t.Fatalf("the complete catalogue must still retain the later MAL part: %#v", complete)
	}
	if err := a.GenerateMappings(root); err != nil {
		t.Fatal(err)
	}
	movieMap, err := repo.GetTMDBMaster(ctx, filepath.Join(root, "tmdb-mal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(movieMap.AnimeMovie) != 1 || movieMap.AnimeMovie[0].TMDBID != 100 {
		t.Fatalf("human correction lost: %#v", movieMap)
	}
	tvMap, err := repo.GetTVDBMaster(ctx, filepath.Join(root, "tvdb-mal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tvMap.Anime) != 1 || tvMap.Anime[0].Tvdbid != 333 {
		t.Fatalf("TVDB correction lost: %#v", tvMap)
	}
}

func TestGenerateMappingsReadsBothMastersBeforeWriting(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := repository.NewFileRepository(zerolog.Nop())
	if err := repo.StoreTMDBMaster(ctx, filepath.Join(root, "tmdb-mal-master.yaml"), &domain.AnimeMovies{AnimeMovie: []domain.AnimeMovie{{MALID: 1, TMDBID: 10, MainTitle: "Example"}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tvdb-mal-master.yaml"), []byte("AnimeMap: ["), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tmdb-mal.yaml", "tvdb-mal.yaml"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("previous output"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{mappingRepo: repo}
	if err := a.GenerateMappings(root); err == nil {
		t.Fatal("expected invalid TVDB master error")
	}
	for _, name := range []string{"tmdb-mal.yaml", "tvdb-mal.yaml"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != "previous output" {
			t.Fatalf("%s changed: %s (%v)", name, got, err)
		}
	}
}

func TestGenerateMappingsExcludesUnavailableMALIDsAndKeepsMasterHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := repository.NewFileRepository(zerolog.Nop())
	tv := &domain.TVDBMap{Anime: []domain.TVDBAnime{{Malid: 1, Tvdbid: 10}, {Malid: 2, Tvdbid: 20}, {Malid: 3, Tvdbid: 30}}}
	movies := &domain.AnimeMovies{AnimeMovie: []domain.AnimeMovie{{MALID: 1, TMDBID: 100}, {MALID: 2, TMDBID: 200}, {MALID: 3, TMDBID: 300}}}
	if err := repo.StoreTVDBMaster(ctx, filepath.Join(root, "tvdb-mal-master.yaml"), tv); err != nil {
		t.Fatal(err)
	}
	if err := repo.StoreTMDBMaster(ctx, filepath.Join(root, "tmdb-mal-master.yaml"), movies); err != nil {
		t.Fatal(err)
	}
	if err := repo.Store(ctx, domain.NewPaths(root).MALUnavailablePath, []domain.Anime{{MalID: 2, MainTitle: "Unavailable"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Store(ctx, domain.NewPaths(root).MalIDPath, []domain.Anime{{MalID: 1, MainTitle: "Live"}}); err != nil {
		t.Fatal(err)
	}
	a := &App{animeRepo: repo, mappingRepo: repo}
	if err := a.GenerateMappings(root); err != nil {
		t.Fatal(err)
	}
	tvExport, err := repo.GetTVDBMaster(ctx, filepath.Join(root, "tvdb-mal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	movieExport, err := repo.GetTMDBMaster(ctx, filepath.Join(root, "tmdb-mal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tvExport.Anime) != 1 || tvExport.Anime[0].Malid != 1 || len(movieExport.AnimeMovie) != 1 || movieExport.AnimeMovie[0].MALID != 1 {
		t.Fatalf("unavailable target exported: %#v %#v", tvExport, movieExport)
	}
	tvMaster, err := repo.GetTVDBMaster(ctx, filepath.Join(root, "tvdb-mal-master.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	movieMaster, err := repo.GetTMDBMaster(ctx, filepath.Join(root, "tmdb-mal-master.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tvMaster.Anime) != 3 || len(movieMaster.AnimeMovie) != 3 {
		t.Fatal("community mapping history lost")
	}
	if err := os.WriteFile(string(domain.NewPaths(root).MALUnavailablePath), []byte("null"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.GenerateMappings(root); err == nil {
		t.Fatal("corrupt unavailable catalogue accepted")
	}
}
