package database

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
)

func TestMALRefreshPreservesIDCachesAndCreationTime(t *testing.T) {
	ctx := context.Background()
	db, err := NewDB(t.TempDir(), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	repo := NewCacheRepo(zerolog.Nop(), db)
	if err := repo.UpsertMAL(ctx, 1, "old", "2000-01-01", "movie"); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertAniDB(ctx, 1, 10); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertTMDB(ctx, 1, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := db.handler.Exec("UPDATE mal_cache SET created_at = '2000-01-01' WHERE mal_id = 1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertMAL(ctx, 1, "new", "2000-02-01", "tv"); err != nil {
		t.Fatal(err)
	}
	a, err := repo.GetAniDBIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, err := repo.GetTMDBIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a[1] != 10 || m[1] != 20 {
		t.Fatalf("cached IDs erased: %v %v", a, m)
	}
	var created, url string
	if err := db.handler.QueryRow("SELECT CAST(created_at AS TEXT), url FROM mal_cache WHERE mal_id = 1").Scan(&created, &url); err != nil {
		t.Fatal(err)
	}
	if created != "2000-01-01" || url != "new" {
		t.Fatalf("metadata = %q %q", created, url)
	}
	if err := repo.DeleteMAL(ctx, 1); err != nil {
		t.Fatal(err)
	}
	a, err = repo.GetAniDBIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, err = repo.GetTMDBIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 0 || len(m) != 0 {
		t.Fatalf("delete did not cascade: %v %v", a, m)
	}
}

func TestIDCacheMigrationCreatesMissingParent(t *testing.T) {
	ctx := context.Background()
	db, err := NewDB(t.TempDir(), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewCacheRepo(zerolog.Nop(), db)
	if err := repo.UpsertAniDB(ctx, 7, 70); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertTMDB(ctx, 7, 80); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertAniDB(ctx, 7, 71); err != nil {
		t.Fatal(err)
	}
	var url string
	if err := db.handler.QueryRow("SELECT url FROM mal_cache WHERE mal_id = 7").Scan(&url); err != nil {
		t.Fatal(err)
	}
	if url != "https://myanimelist.net/anime/7" {
		t.Fatal(url)
	}
	a, err := repo.GetAniDBIDs(ctx)
	if err != nil || a[7] != 71 {
		t.Fatalf("updated cache = %v (%v)", a, err)
	}
}
