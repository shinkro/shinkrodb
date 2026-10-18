package domain

import "path/filepath"

type AnimeFile string

const (
	MalIDFile          AnimeFile = "malid.json"
	MALUnavailableFile AnimeFile = "mal-unavailable.json"
	AniDBFile          AnimeFile = "malid-anidbid.json"
	TVDBFile           AnimeFile = "malid-anidbid-tvdbid.json"
	TMDBFile           AnimeFile = "malid-anidbid-tvdbid-tmdbid.json"
	ShinkroFile        AnimeFile = "for-shinkro.json"
)

type AnimePath string

// Paths holds all the file paths for anime data
type Paths struct {
	RootDir            string
	MalIDPath          AnimePath
	MALUnavailablePath AnimePath
	AniDBPath          AnimePath
	TVDBPath           AnimePath
	TMDBPath           AnimePath
	ShinkroPath        AnimePath
}

// NewPaths creates a new Paths instance with all paths initialized
func NewPaths(rootDir string) *Paths {
	rootDir = filepath.Join(rootDir, "shinkrodb")
	return &Paths{
		RootDir:            rootDir,
		MalIDPath:          makeAnimePath(rootDir, MalIDFile),
		MALUnavailablePath: makeAnimePath(rootDir, MALUnavailableFile),
		AniDBPath:          makeAnimePath(rootDir, AniDBFile),
		TVDBPath:           makeAnimePath(rootDir, TVDBFile),
		TMDBPath:           makeAnimePath(rootDir, TMDBFile),
		ShinkroPath:        makeAnimePath(rootDir, ShinkroFile),
	}
}

func makeAnimePath(rootDir string, af AnimeFile) AnimePath {
	return AnimePath(filepath.Join(rootDir, string(af)))
}
