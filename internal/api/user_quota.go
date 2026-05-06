package api

import (
	"context"

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
