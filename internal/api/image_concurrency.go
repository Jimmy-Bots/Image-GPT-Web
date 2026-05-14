package api

import (
	"context"
	"fmt"

	"gpt-image-web/internal/storage"
)

const defaultUserImageConcurrency = 4

func (s *Server) userImageConcurrencyLimit(ctx context.Context) int {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		if s.cfg.ImageUserConcurrency > 0 {
			return s.cfg.ImageUserConcurrency
		}
		return defaultUserImageConcurrency
	}
	if limit := intMapValue(settings, "image_user_concurrency"); limit > 0 {
		return limit
	}
	if s.cfg.ImageUserConcurrency > 0 {
		return s.cfg.ImageUserConcurrency
	}
	return defaultUserImageConcurrency
}

func (s *Server) ensureUserImageConcurrency(ctx context.Context, ownerID string) error {
	limit := s.userImageConcurrencyLimit(ctx)
	if limit <= 0 {
		return nil
	}
	active, err := s.store.CountActiveImageTasksByOwner(ctx, ownerID)
	if err != nil {
		return err
	}
	if active >= limit {
		return fmt.Errorf("user image concurrency exceeded: %d/%d", active, limit)
	}
	return nil
}

var _ = storage.ErrNotFound
