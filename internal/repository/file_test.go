package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
)

func TestCancelledWritePreservesExistingOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "output.json")
	if err := os.WriteFile(path, []byte("previous"), 0644); err != nil {
		t.Fatal(err)
	}
	repo := NewFileRepository(zerolog.Nop())
	if err := repo.Store(ctx, domain.AnimePath(path), []domain.Anime{{MalID: 1}}); err == nil {
		t.Fatal("expected cancellation")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "previous" {
		t.Fatalf("output changed: %s (%v)", got, err)
	}
}

func TestFailedReplacementCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.json")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(context.Background(), path, []byte("replacement")); err == nil {
		t.Fatal("expected rename failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "output.json" || !entries[0].IsDir() {
		t.Fatalf("temporary file or damaged destination: %v", entries)
	}
}

func TestMappingReadersRejectUnknownFieldsAndExtraDocuments(t *testing.T) {
	for _, tvdb := range []bool{false, true} {
		for _, kind := range []string{"unknown root", "unknown field", "extra document", "missing list", "null list"} {
			t.Run(fmt.Sprintf("%s/tvdb=%t", kind, tvdb), func(t *testing.T) {
				root, row := "animeMovies", "{mainTitle: X, malid: 1, tmdbid: 10}"
				if tvdb {
					root, row = "AnimeMap", "{title: X, malid: 1, tvdbid: 10}"
				}
				contents := root + ": [" + row + "]\n"
				switch kind {
				case "unknown root":
					contents = "typo: []\n"
				case "unknown field":
					contents = strings.Replace(contents, "malid:", "maldi:", 1)
				case "extra document":
					contents += "---\n" + root + ": []\n"
				case "missing list":
					contents = "{}"
				case "null list":
					contents = root + ": null\n"
				}
				path := filepath.Join(t.TempDir(), "master.yaml")
				if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
					t.Fatal(err)
				}
				repo := NewFileRepository(zerolog.Nop())
				var err error
				if tvdb {
					_, err = repo.GetTVDBMaster(context.Background(), path)
				} else {
					_, err = repo.GetTMDBMaster(context.Background(), path)
				}
				if err == nil {
					t.Fatalf("accepted %q", contents)
				}
			})
		}
	}
}
