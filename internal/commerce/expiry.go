package commerce

import (
	"context"
	"database/sql"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// releaseCheckoutTx releases a pending checkout and its hold together. A stale
// checkout can never reactivate a paid/lost lead or a newer reserved checkout.
func (s *Service) releaseCheckoutTx(ctx context.Context, tx *sql.Tx, c checkoutRecord, reason string, now time.Time) (bool, error) {
	target := domain.CheckoutCancelled
	if reason == "expired" {
		target = domain.CheckoutExpired
	}
	result, err := tx.ExecContext(ctx, `UPDATE checkouts SET status=?,updated_at=? WHERE id=? AND status='pending'`, target, now.UnixMilli(), c.ID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	result, err = tx.ExecContext(ctx, `UPDATE reservations SET status='released',released_reason=?,updated_at=? WHERE id=? AND seller_id=? AND lead_id=? AND item_id=? AND status='active'`, reason, now.UnixMilli(), c.ReservationID, c.SellerID, c.LeadID, c.ItemID)
	if err != nil {
		return false, err
	}
	n, err = result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, commerceProblem(contracts.InvalidState, "checkout reservation is inconsistent")
	}
	_, err = tx.ExecContext(ctx, `UPDATE leads SET state='active',version=version+1,updated_at=? WHERE id=? AND seller_id=? AND state='reserved' AND NOT EXISTS(SELECT 1 FROM checkouts WHERE lead_id=? AND status='pending')`, now.UnixMilli(), c.LeadID, c.SellerID, c.LeadID)
	return true, err
}

func (s *Service) expireCheckoutsTx(ctx context.Context, tx *sql.Tx, now time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM checkouts WHERE status='pending' AND expires_at<=? ORDER BY seq`, now.UnixMilli())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		c, err := s.checkoutByIDTx(ctx, tx, id)
		if err != nil {
			return 0, err
		}
		released, err := s.releaseCheckoutTx(ctx, tx, c, "expired", now)
		if err != nil {
			return 0, err
		}
		if released {
			count++
		}
	}
	return count, nil
}

// ExpireCheckouts is safe for periodic invocation and is also used lazily on
// create/read/pay paths. The caller owns scheduling/lifecycle.
func (s *Service) ExpireCheckouts(ctx context.Context) (int, error) {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count, err := s.expireCheckoutsTx(ctx, tx, s.now())
	if err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

// CancelCheckout is seller-scoped and idempotent for an already released
// checkout. Paid checkout cannot be cancelled through this mock MVP operation.
func (s *Service) CancelCheckout(ctx context.Context, sellerID, checkoutID string) (domain.Checkout, error) {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return domain.Checkout{}, err
	}
	defer tx.Rollback()
	c, err := s.checkoutByIDTx(ctx, tx, checkoutID)
	if err != nil {
		return domain.Checkout{}, err
	}
	if c.SellerID != sellerID {
		return domain.Checkout{}, commerceProblem(contracts.NotFound, "checkout not found")
	}
	if c.Status == domain.CheckoutPaid {
		return domain.Checkout{}, commerceProblem(contracts.InvalidState, "paid checkout cannot be cancelled")
	}
	if c.Status == domain.CheckoutPending {
		reason := "cancelled"
		if !s.now().Before(c.ExpiresAt) {
			reason = "expired"
		}
		if _, err = s.releaseCheckoutTx(ctx, tx, c, reason, s.now()); err != nil {
			return domain.Checkout{}, err
		}
		c.Status = domain.CheckoutCancelled
		if reason == "expired" {
			c.Status = domain.CheckoutExpired
		}
	}
	return c.Checkout, tx.Commit()
}
