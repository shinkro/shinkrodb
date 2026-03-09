package domain

import "context"

// NotificationService defines the interface for notification services
type NotificationService interface {
	// SendSuccess sends a success notification with statistics
	SendSuccess(ctx context.Context, stats Statistics) error
	
	// SendError sends an error notification with error details
	SendError(ctx context.Context, err error) error
}

// Statistics holds the final statistics for the run
type Statistics struct {
	TotalMALIDs           int
	MALIDsWithAniDB       int
	TotalMovies           int
	MoviesWithTMDB        int
	TotalTVShows          int
	TVShowsWithTVDB       int
	AniDBCoveragePercent  float64
	TMDBCoveragePercent   float64
	TVDBCoveragePercent   float64
	DupeCount             int
	PreservedMALIDs       int
}

