package notification

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
)

// Service is a composite notification service that can send notifications
// through multiple channels
type Service struct {
	discord *DiscordService
}

// NewService creates a new notification service
func NewService(log zerolog.Logger, webhookURL string) domain.NotificationService {
	var discord *DiscordService
	if webhookURL != "" {
		discord = NewDiscordService(log, webhookURL)
	}

	return &Service{
		discord: discord,
	}
}

// SendSuccess sends success notifications through all configured channels
func (s *Service) SendSuccess(ctx context.Context, stats domain.Statistics) error {
	if s.discord != nil {
		if err := s.discord.SendSuccess(ctx, stats); err != nil {
			return err
		}
	}
	return nil
}

// SendError sends error notifications through all configured channels
func (s *Service) SendError(ctx context.Context, err error) error {
	if s.discord != nil {
		if err := s.discord.SendError(ctx, err); err != nil {
			return err
		}
	}
	return nil
}

