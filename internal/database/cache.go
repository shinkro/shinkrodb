package database

import (
	"context"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
)

// CacheRepo implements domain.CacheRepo interface
type CacheRepo struct {
	log zerolog.Logger
	db  *DB
}

// NewCacheRepo creates a new cache repository
func NewCacheRepo(log zerolog.Logger, db *DB) domain.CacheRepo {
	return &CacheRepo{
		log: log.With().Str("repo", "cache").Logger(),
		db:  db,
	}
}

// UpsertMAL inserts or updates a MAL cache entry
func (r *CacheRepo) UpsertMAL(ctx context.Context, malID int, url, releaseDate, animeType string) error {
	now := time.Now().Format(time.RFC3339)

	queryBuilder := r.db.squirrel.
		Insert("mal_cache").
		Columns("mal_id", "url", "release_date", "type", "cached_at", "last_used").
		Values(malID, url, releaseDate, animeType, now, now).
		Suffix(`ON CONFLICT(mal_id) DO UPDATE SET
			url = excluded.url, release_date = excluded.release_date, type = excluded.type,
			cached_at = excluded.cached_at, last_used = excluded.last_used`)

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return errors.Wrap(err, "error building query")
	}

	r.log.Trace().Str("query", query).Interface("args", args).Msg("UpsertMAL")

	_, err = r.db.handler.ExecContext(ctx, query, args...)
	if err != nil {
		return errors.Wrap(err, "error executing query")
	}

	return nil
}

// GetAniDBIDs returns a map of MAL ID to AniDB ID for entries that have AniDB IDs
func (r *CacheRepo) GetAniDBIDs(ctx context.Context) (map[int]int, error) {
	queryBuilder := r.db.squirrel.
		Select("mal_id", "anidb_id").
		From("anidb_cache")

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "error building query")
	}

	r.log.Trace().Str("query", query).Interface("args", args).Msg("GetAniDBIDs")

	rows, err := r.db.handler.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrap(err, "error executing query")
	}
	defer rows.Close()

	result := make(map[int]int)
	for rows.Next() {
		var malID, anidbID int
		if err := rows.Scan(&malID, &anidbID); err != nil {
			return nil, errors.Wrap(err, "error scanning row")
		}
		result[malID] = anidbID
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "error iterating rows")
	}

	return result, nil
}

// UpsertAniDB inserts or updates an AniDB cache entry
func (r *CacheRepo) UpsertAniDB(ctx context.Context, malID, anidbID int) error {
	return r.upsertID(ctx, "anidb_cache", "anidb_id", "had_anidb_id", malID, anidbID)
}

// GetTMDBIDs returns a map of MAL ID to TMDB ID for entries that have TMDB IDs
func (r *CacheRepo) GetTMDBIDs(ctx context.Context) (map[int]int, error) {
	queryBuilder := r.db.squirrel.
		Select("mal_id", "tmdb_id").
		From("tmdb_cache")

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "error building query")
	}

	r.log.Trace().Str("query", query).Interface("args", args).Msg("GetTMDBIDs")

	rows, err := r.db.handler.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrap(err, "error executing query")
	}
	defer rows.Close()

	result := make(map[int]int)
	for rows.Next() {
		var malID, tmdbID int
		if err := rows.Scan(&malID, &tmdbID); err != nil {
			return nil, errors.Wrap(err, "error scanning row")
		}
		result[malID] = tmdbID
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "error iterating rows")
	}

	return result, nil
}

// UpsertTMDB inserts or updates a TMDB cache entry
func (r *CacheRepo) UpsertTMDB(ctx context.Context, malID, tmdbID int) error {
	return r.upsertID(ctx, "tmdb_cache", "tmdb_id", "has_tmdb_id", malID, tmdbID)
}

// upsertID also supports cache migrations where MAL metadata is not available.
// An existing parent is never replaced or cleared while creating a missing one.
func (r *CacheRepo) upsertID(ctx context.Context, table, idColumn, hasColumn string, malID, id int) error {
	now := time.Now().Format(time.RFC3339)
	tx, err := r.db.handler.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin cache update")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO mal_cache (mal_id, url, cached_at, last_used)
		VALUES (?, ?, ?, ?) ON CONFLICT(mal_id) DO NOTHING`,
		malID, fmt.Sprintf("https://myanimelist.net/anime/%d", malID), now, now); err != nil {
		return errors.Wrap(err, "ensure MAL cache parent")
	}

	queryBuilder := r.db.squirrel.
		Insert(table).
		Columns("mal_id", idColumn, hasColumn, "cached_at", "last_used").
		Values(malID, id, id > 0, now, now).
		Suffix(fmt.Sprintf(`ON CONFLICT(mal_id) DO UPDATE SET
			%s = excluded.%s, %s = excluded.%s,
			cached_at = excluded.cached_at, last_used = excluded.last_used`, idColumn, idColumn, hasColumn, hasColumn))

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return errors.Wrap(err, "error building query")
	}

	_, err = tx.ExecContext(ctx, query, args...)
	if err != nil {
		return errors.Wrap(err, "error executing query")
	}

	return tx.Commit()
}

// GetEntriesByReleaseYear returns cache entries for anime released in a specific year
func (r *CacheRepo) GetEntriesByReleaseYear(ctx context.Context, year int) ([]*domain.MALCacheEntry, error) {
	queryBuilder := r.db.squirrel.
		Select("m.mal_id", "m.release_date", "m.type").
		From("mal_cache m").
		LeftJoin("anidb_cache a ON m.mal_id = a.mal_id").
		Where(sq.And{
			sq.Expr("m.release_date IS NOT NULL"),
			sq.NotEq{"m.release_date": ""},
			sq.Or{
				sq.Expr("CAST(strftime('%Y', m.release_date) AS INTEGER) = ?", year),
				sq.Expr("CAST(SUBSTR(m.release_date, 1, 4) AS INTEGER) = ?", year),
			},
			sq.Or{
				sq.Expr("a.mal_id IS NULL"),
				sq.Eq{"a.anidb_id": 0},
			},
		})

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "error building query")
	}

	r.log.Trace().Str("query", query).Interface("args", args).Msg("GetEntriesByReleaseYear")

	rows, err := r.db.handler.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrap(err, "error executing query")
	}
	defer rows.Close()

	var entries []*domain.MALCacheEntry
	for rows.Next() {
		entry := &domain.MALCacheEntry{}
		if err := rows.Scan(&entry.MalID, &entry.ReleaseDate, &entry.Type); err != nil {
			return nil, errors.Wrap(err, "error scanning row")
		}
		entries = append(entries, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "error iterating rows")
	}

	return entries, nil
}

// DeleteMAL deletes a MAL cache entry (cascade deletes AniDB and TMDB entries)
func (r *CacheRepo) DeleteMAL(ctx context.Context, malID int) error {
	queryBuilder := r.db.squirrel.
		Delete("mal_cache").
		Where(sq.Eq{"mal_id": malID})

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return errors.Wrap(err, "error building delete query")
	}

	r.log.Trace().Str("query", query).Interface("args", args).Msg("DeleteMAL")

	_, err = r.db.handler.ExecContext(ctx, query, args...)
	if err != nil {
		return errors.Wrap(err, "error executing delete query")
	}

	return nil
}
