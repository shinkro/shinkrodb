package database

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	sq "github.com/Masterminds/squirrel"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	_ "modernc.org/sqlite"
)

// DB represents the database connection following shinkro's pattern
type DB struct {
	handler  *sql.DB
	log      zerolog.Logger
	lock     sync.RWMutex
	squirrel sq.StatementBuilderType
}

// NewDB creates a new database connection following shinkro's pattern
func NewDB(dir string, log zerolog.Logger) (*DB, error) {
	db := &DB{
		log:      log.With().Str("module", "database").Logger(),
		squirrel: sq.StatementBuilder.PlaceholderFormat(sq.Dollar),
	}

	var (
		err error
	)

	// Build PRAGMA statements as DSN parameters (following autobrr pattern)
	pragmaSlice := []string{
		// Set busy timeout to 1 second. It forces the driver to keep retrying a locked operation before giving up.
		"?_pragma=busy_timeout(1000)",
		// Enable Write-Ahead Logging (WAL). SQLite performs better with the WAL because it allows
		// multiple readers to operate while data is being written.
		"_pragma=journal_mode(WAL)",
		// Default is FULL. Reducing this to NORMAL saves a significant amount of disk I/O (fsyncs).
		"_pragma=synchronous(NORMAL)",
		"_pragma=foreign_keys(1)",
	}

	pragmas := strings.Join(pragmaSlice, "&")
	DSN := filepath.Join(dir, "shinkrodb.db") + pragmas

	db.handler, err = sql.Open("sqlite", DSN)
	if err != nil {
		return nil, errors.Wrap(err, "unable to connect to database")
	}

	// Ensure schema is up to date (migrates if needed)
	if err := db.Migrate(); err != nil {
		db.handler.Close()
		return nil, errors.Wrap(err, "failed to migrate schema")
	}

	return db, nil
}

// Migrate handles database schema creation and migrations using versioning
// Follows the same pattern as shinkro's database migration strategy
func (db *DB) Migrate() error {
	db.lock.Lock()
	defer db.lock.Unlock()

	var version int
	if err := db.handler.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return errors.Wrap(err, "failed to query schema version")
	}

	if version == len(cacheMigrations) {
		return nil
	} else if version > len(cacheMigrations) {
		return errors.Errorf("cache database schema version (%d) is newer than supported (%d)", version, len(cacheMigrations))
	}

	db.log.Info().Msgf("Beginning database schema upgrade from version %v to version: %v", version, len(cacheMigrations))

	tx, err := db.handler.Begin()
	if err != nil {
		return errors.Wrap(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	if version == 0 {
		// Create initial schema
		if _, err := tx.Exec(cacheSchema); err != nil {
			return errors.Wrap(err, "failed to initialize schema")
		}
		db.log.Info().Msg("Created initial cache database schema")
	} else {
		// Apply incremental migrations
		for i := version; i < len(cacheMigrations); i++ {
			if cacheMigrations[i] == "" {
				continue // Skip empty migration (version 0)
			}
			db.log.Info().Msgf("Upgrading cache database schema to version: %v", i+1)
			if _, err := tx.Exec(cacheMigrations[i]); err != nil {
				return errors.Wrapf(err, "failed to execute migration #%v", i)
			}
		}
	}

	// Update schema version
	_, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(cacheMigrations)))
	if err != nil {
		return errors.Wrap(err, "failed to bump schema version")
	}

	db.log.Info().Msgf("Database schema upgraded to version: %v", len(cacheMigrations))
	return tx.Commit()
}

// Close closes the database connection
func (db *DB) Close() error {
	if _, err := db.handler.Exec(`PRAGMA optimize;`); err != nil {
		return errors.Wrap(err, "query planner optimization")
	}

	return db.handler.Close()
}

// Ping checks if the database connection is alive
func (db *DB) Ping() error {
	return db.handler.Ping()
}

// BeginTx starts a new transaction
func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := db.handler.BeginTx(ctx, opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to begin transaction")
	}

	return &Tx{
		Tx:      tx,
		handler: db,
	}, nil
}

// Tx represents a database transaction
type Tx struct {
	*sql.Tx
	handler *DB
}
