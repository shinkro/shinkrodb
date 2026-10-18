package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
	"gopkg.in/yaml.v3"
)

// FileRepository implements domain.AnimeRepository and domain.MappingRepository using file storage
type FileRepository struct {
	log zerolog.Logger
}

// NewFileRepository creates a new file-based repository
func NewFileRepository(log zerolog.Logger) *FileRepository {
	return &FileRepository{
		log: log.With().Str("module", "repository").Logger(),
	}
}

// Ensure FileRepository implements both interfaces
var _ domain.AnimeRepository = (*FileRepository)(nil)
var _ domain.MappingRepository = (*FileRepository)(nil)

// Get retrieves anime data from a file
func (r *FileRepository) Get(ctx context.Context, path domain.AnimePath) ([]domain.Anime, error) {
	a := []domain.Anime{}

	// Check if path exists and is a file (not a directory)
	info, err := os.Stat(string(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file does not exist: %s: %w", path, err)
		}
		return nil, fmt.Errorf("failed to stat file %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file: %s", path)
	}

	f, err := os.Open(string(path))
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", path, err)
	}
	defer f.Close()

	body, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	err = json.Unmarshal(body, &a)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal json from %s: %w", path, err)
	}
	if a == nil {
		return nil, fmt.Errorf("catalogue %s must contain a JSON array", path)
	}

	return a, nil
}

// Store saves anime data to a file
func (r *FileRepository) Store(ctx context.Context, path domain.AnimePath, anime []domain.Anime) error {
	j, err := json.MarshalIndent(anime, "", "   ")
	if err != nil {
		return fmt.Errorf("failed to marshal anime data: %w", err)
	}

	if err := writeFile(ctx, string(path), j); err != nil {
		return err
	}

	r.log.Debug().Str("path", string(path)).Int("count", len(anime)).Msg("stored anime data")
	return nil
}

// GetTMDBMaster retrieves TMDB master mapping from a file
func (r *FileRepository) GetTMDBMaster(ctx context.Context, path string) (*domain.AnimeMovies, error) {
	am := &domain.AnimeMovies{}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("file does not exist: %w", err)
	}

	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	err = decodeMapping(b, am)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
	}
	if am.AnimeMovie == nil {
		return nil, fmt.Errorf("missing or null animeMovies list in %s", path)
	}

	return am, nil
}

// StoreTMDBMaster saves TMDB master mapping to a file
func (r *FileRepository) StoreTMDBMaster(ctx context.Context, path string, movies *domain.AnimeMovies) error {
	b, err := yaml.Marshal(movies)
	if err != nil {
		return fmt.Errorf("failed to marshal yaml: %w", err)
	}

	text := string(b)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.Contains(line, "malid") {
			lines[i] += "\n"
		}
	}

	modifiedText := strings.Join(lines, "\n")
	if err := writeFile(ctx, path, []byte(modifiedText)); err != nil {
		return err
	}

	r.log.Debug().Str("path", path).Msg("stored TMDB master")
	return nil
}

// GetTVDBMaster retrieves TVDB master mapping from a file
func (r *FileRepository) GetTVDBMaster(ctx context.Context, path string) (*domain.TVDBMap, error) {
	am := &domain.TVDBMap{}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("file does not exist: %w", err)
	}

	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	err = decodeMapping(b, am)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
	}
	if am.Anime == nil {
		return nil, fmt.Errorf("missing or null AnimeMap list in %s", path)
	}

	return am, nil
}

// StoreTVDBMaster saves TVDB master mapping to a file
func (r *FileRepository) StoreTVDBMaster(ctx context.Context, path string, map_ *domain.TVDBMap) error {
	b, err := yaml.Marshal(map_)
	if err != nil {
		return fmt.Errorf("failed to marshal yaml: %w", err)
	}

	text := string(b)
	lines := strings.Split(text, "\n")
	malidFound := false
	for i, line := range lines {
		if strings.Contains(line, "malid") {
			if malidFound {
				lines[i-1] += "\n"
			} else {
				malidFound = true
			}
		}
	}

	modifiedText := strings.Join(lines, "\n")
	if err := writeFile(ctx, path, []byte(modifiedText)); err != nil {
		return err
	}

	r.log.Debug().Str("path", path).Msg("stored TVDB master")
	return nil
}

func decodeMapping(data []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("mapping file must contain exactly one YAML document")
	}
	return nil
}

// writeFile replaces an output only after the complete new file has been written.
func writeFile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".shinkrodb-*")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Chmod(0644); err != nil {
		return fmt.Errorf("set output permissions: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
