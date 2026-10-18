package tvdb

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
	"github.com/shinkro/shinkrodb/internal/repository"
)

func TestMasterPreservesOmittedAndUnfinishedMappings(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := repository.NewFileRepository(zerolog.Nop())
	paths := domain.NewPaths(root)
	masterPath := filepath.Join(root, "tvdb-mal-master.yaml")
	rules := []domain.AnimeMapping{{TvdbSeason: 0, MappingType: "explicit", ExplicitEpisodes: map[int]int{7: 1}}}
	existing := &domain.TVDBMap{Anime: []domain.TVDBAnime{
		{Malid: 1, Title: "Old title", Type: "tv", Tvdbid: 100, UseMapping: true, AnimeMapping: rules},
		{Malid: 2, Title: "Omitted", Type: "ova", Tvdbid: 200},
		{Malid: 3, Title: "Unfinished", Type: "tv", UseMapping: true, AnimeMapping: rules},
	}}
	if err := repo.StoreTVDBMaster(ctx, masterPath, existing); err != nil {
		t.Fatal(err)
	}
	if err := repo.Store(ctx, paths.MalIDPath, []domain.Anime{
		{MalID: 1, MainTitle: "New title", Type: "tv"}, {MalID: 3, MainTitle: "Unfinished", Type: "tv"},
		{MalID: 4, MainTitle: "New entry", Type: "ona"}, {MalID: 4, MainTitle: "New entry", Type: "ona"},
	}); err != nil {
		t.Fatal(err)
	}
	s := NewService(zerolog.Nop(), repo, repo, paths).(*service)
	if err := s.createAndUpdateMaster(ctx, root); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetTVDBMaster(ctx, masterPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Anime) != 4 {
		t.Fatalf("master = %#v", got)
	}
	if got.Anime[0].Title != "New title" || !reflect.DeepEqual(got.Anime[0].AnimeMapping, rules) {
		t.Fatalf("existing rules lost: %#v", got.Anime[0])
	}
	if got.Anime[1].Tvdbid != 200 || !reflect.DeepEqual(got.Anime[2].AnimeMapping, rules) {
		t.Fatalf("omitted/unfinished rules lost: %#v", got)
	}
	unmapped, err := repo.GetTVDBMaster(ctx, filepath.Join(root, "tvdb-mal-unmapped.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(unmapped.Anime) != 2 || unmapped.Anime[0].Malid != 3 || unmapped.Anime[1].Malid != 4 {
		t.Fatalf("unmapped = %#v", unmapped)
	}
}

func TestInvalidMasterDoesNotOverwriteOutputs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := repository.NewFileRepository(zerolog.Nop())
	paths := domain.NewPaths(root)
	if err := repo.Store(ctx, paths.MalIDPath, []domain.Anime{{MalID: 1, MainTitle: "Example"}}); err != nil {
		t.Fatal(err)
	}
	master := filepath.Join(root, "tvdb-mal-master.yaml")
	unmapped := filepath.Join(root, "tvdb-mal-unmapped.yaml")
	for p, text := range map[string]string{master: "AnimeMap: [", unmapped: "previous output"} {
		if err := os.WriteFile(p, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(zerolog.Nop(), repo, repo, paths).(*service)
	if err := s.createAndUpdateMaster(ctx, root); err == nil {
		t.Fatal("expected error")
	}
	for p, want := range map[string]string{master: "AnimeMap: [", unmapped: "previous output"} {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Fatalf("output changed: %s (%v)", got, err)
		}
	}
}
