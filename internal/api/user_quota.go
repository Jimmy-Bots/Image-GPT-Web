package api

import (
	"context"
	"time"

	"gpt-image-web/internal/domain"
	"gpt-image-web/internal/storage"
)

func (s *Server) reserveImageQuota(ctx context.Context, identity Identity, amount int) (domain.User, domain.UserQuotaReceipt, error) {
	return s.store.ReserveUserQuota(ctx, identity.ID, amount)
}

func (s *Server) refundImageQuota(ctx context.Context, identity Identity, receipt domain.UserQuotaReceipt) {
	if receipt.Total < 1 {
		return
	}
	_, _ = s.store.RefundUserQuota(ctx, identity.ID, receipt)
}

func (s *Server) refundImageQuotaWithResult(ctx context.Context, identity Identity, receipt domain.UserQuotaReceipt) (domain.User, bool) {
	if receipt.Total < 1 {
		return domain.User{}, false
	}
	user, err := s.store.RefundUserQuota(ctx, identity.ID, receipt)
	if err != nil {
		return domain.User{}, false
	}
	return user, true
}

func (s *Server) addImageQuotaUsage(ctx context.Context, identity Identity, amount int) (domain.User, bool) {
	if amount <= 0 {
		return domain.User{}, false
	}
	user, err := s.store.AddUserQuotaUsage(ctx, identity.ID, amount, time.Now())
	if err != nil {
		return domain.User{}, false
	}
	return user, true
}

func quotaRefundPortion(receipt domain.UserQuotaReceipt, refund int) domain.UserQuotaReceipt {
	if refund <= 0 || receipt.Total <= 0 {
		return domain.UserQuotaReceipt{}
	}
	if refund > receipt.Total {
		refund = receipt.Total
	}
	temporary := receipt.Temporary
	if temporary > refund {
		temporary = refund
	}
	remaining := refund - temporary
	permanent := receipt.Permanent
	if permanent > remaining {
		permanent = remaining
	}
	return domain.UserQuotaReceipt{
		Permanent:     permanent,
		Temporary:     temporary,
		TemporaryDate: receipt.TemporaryDate,
		Total:         permanent + temporary,
	}
}

var _ = storage.ErrNotFound
