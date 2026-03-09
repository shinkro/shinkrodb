package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/varoOP/shinkrodb/internal/domain"
)

// DiscordService implements NotificationService for Discord webhooks
type DiscordService struct {
	log        zerolog.Logger
	webhookURL string
	httpClient *http.Client
}

// NewDiscordService creates a new Discord notification service
func NewDiscordService(log zerolog.Logger, webhookURL string) *DiscordService {
	return &DiscordService{
		log:        log.With().Str("module", "notification").Str("type", "discord").Logger(),
		webhookURL: webhookURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SendSuccess sends a success notification with statistics
func (s *DiscordService) SendSuccess(ctx context.Context, stats domain.Statistics) error {
	if s.webhookURL == "" {
		return nil // No webhook configured, skip silently
	}

	embed := discordEmbed{
		Title:       "ShinkroDB Run Completed Successfully",
		Description: "Database update completed successfully",
		Color:       0x00ff00, // Green
		Timestamp:   time.Now().Format(time.RFC3339),
		Fields: s.buildSuccessFields(stats),
	}

	payload := discordWebhook{
		Embeds: []discordEmbed{embed},
	}

	return s.sendWebhook(ctx, payload)
}

func (s *DiscordService) buildSuccessFields(stats domain.Statistics) []discordField {
	fields := []discordField{
		{
			Name:   "Total MAL IDs",
			Value:  fmt.Sprintf("%d", stats.TotalMALIDs),
			Inline: true,
		},
		{
			Name:   "AniDB Coverage",
			Value:  fmt.Sprintf("%d (%.1f%%)", stats.MALIDsWithAniDB, stats.AniDBCoveragePercent),
			Inline: true,
		},
		{
			Name:   "Movies",
			Value:  fmt.Sprintf("%d total, %d with TMDB (%.1f%%)", stats.TotalMovies, stats.MoviesWithTMDB, stats.TMDBCoveragePercent),
			Inline: false,
		},
		{
			Name:   "TV Shows",
			Value:  fmt.Sprintf("%d total, %d with TVDB (%.1f%%)", stats.TotalTVShows, stats.TVShowsWithTVDB, stats.TVDBCoveragePercent),
			Inline: false,
		},
		{
			Name:   "Duplicates Removed",
			Value:  fmt.Sprintf("%d", stats.DupeCount),
			Inline: true,
		},
	}
	if stats.PreservedMALIDs > 0 {
		fields = append(fields, discordField{
			Name:   "Preserved MAL IDs (API omission)",
			Value:  fmt.Sprintf("%d", stats.PreservedMALIDs),
			Inline: true,
		})
	}
	return fields
}

// SendError sends an error notification with error details
func (s *DiscordService) SendError(ctx context.Context, err error) error {
	if s.webhookURL == "" {
		return nil // No webhook configured, skip silently
	}

	embed := discordEmbed{
		Title:       "ShinkroDB Run Failed",
		Description: fmt.Sprintf("Database update failed with error:\n```%s```", err.Error()),
		Color:       0xff0000, // Red
		Timestamp:   time.Now().Format(time.RFC3339),
	}

	payload := discordWebhook{
		Embeds: []discordEmbed{embed},
	}

	return s.sendWebhook(ctx, payload)
}

// sendWebhook sends a webhook payload to Discord
func (s *DiscordService) sendWebhook(ctx context.Context, payload discordWebhook) error {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrap(err, "failed to marshal webhook payload")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return errors.Wrap(err, "failed to create webhook request")
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "failed to send webhook request")
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook request failed with status %d", resp.StatusCode)
	}

	s.log.Debug().Msg("Discord notification sent successfully")
	return nil
}

// discordWebhook represents a Discord webhook payload
type discordWebhook struct {
	Embeds []discordEmbed `json:"embeds"`
}

// discordEmbed represents a Discord embed
type discordEmbed struct {
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	Color       int          `json:"color"`
	Timestamp   string       `json:"timestamp,omitempty"`
	Fields      []discordField `json:"fields,omitempty"`
}

// discordField represents a Discord embed field
type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

