package commerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func paymentForCheckoutTx(ctx context.Context, tx *sql.Tx, checkoutID string) (Payment, error) {
	var p Payment
	var paidAt int64
	err := tx.QueryRowContext(ctx, `SELECT id,checkout_id,lead_id,amount_idr,quantity,currency,created_at FROM payments WHERE checkout_id=?`, checkoutID).Scan(&p.ID, &p.CheckoutID, &p.LeadID, &p.AmountIDR, &p.Quantity, &p.Currency, &paidAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, commerceProblem(contracts.InvalidState, "paid checkout has no receipt")
	}
	if err != nil {
		return p, err
	}
	p.PaidAt = time.UnixMilli(paidAt).UTC()
	return p, nil
}

// PayCheckout commits mock payment, physical stock/ledger, reservation and lead
// status in one transaction. The browser supplies no commercial terms.
func (s *Service) PayCheckout(ctx context.Context, token, actionKey string) (Payment, error) {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return Payment{}, err
	}
	defer tx.Rollback()
	c, err := s.checkoutByTokenTx(ctx, tx, token)
	if err != nil {
		return Payment{}, err
	}
	request := struct{ CheckoutID string }{c.ID}
	raw, found, err := replayAction(ctx, tx, c.SellerID, actionKey, "pay_checkout", request)
	if err != nil {
		return Payment{}, err
	}
	if found {
		var saved Payment
		if err = json.Unmarshal(raw, &saved); err != nil {
			return Payment{}, err
		}
		return saved, tx.Commit()
	}
	if c.Status == domain.CheckoutPaid {
		receipt, err := paymentForCheckoutTx(ctx, tx, c.ID)
		if err != nil {
			return Payment{}, err
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return Payment{}, err
		}
		if err = s.saveAction(ctx, tx, c.SellerID, actionKey, "pay_checkout", request, raw); err != nil {
			return Payment{}, err
		}
		return receipt, tx.Commit()
	}
	now := time.UnixMilli(s.now().UnixMilli()).UTC()
	if c.Status == domain.CheckoutPending && !now.Before(c.ExpiresAt) {
		if _, err = s.releaseCheckoutTx(ctx, tx, c, "expired", now); err != nil {
			return Payment{}, err
		}
		// The expiry transition must persist even though this payment returns a
		// business error. Returning before Commit would resurrect the expired hold.
		if err = tx.Commit(); err != nil {
			return Payment{}, err
		}
		return Payment{}, commerceProblem(contracts.CheckoutExpired, "checkout has expired")
	}
	if c.Status == domain.CheckoutExpired {
		return Payment{}, commerceProblem(contracts.CheckoutExpired, "checkout has expired")
	}
	if c.Status != domain.CheckoutPending {
		return Payment{}, commerceProblem(contracts.InvalidState, "checkout is not payable")
	}
	if _, err = s.expireCheckoutsTx(ctx, tx, now); err != nil {
		return Payment{}, err
	}
	var reservationState string
	var reservationQuantity int
	var reservationExpiry int64
	err = tx.QueryRowContext(ctx, `SELECT status,quantity,expires_at FROM reservations WHERE id=? AND seller_id=? AND lead_id=? AND item_id=?`, c.ReservationID, c.SellerID, c.LeadID, c.ItemID).Scan(&reservationState, &reservationQuantity, &reservationExpiry)
	if errors.Is(err, sql.ErrNoRows) {
		return Payment{}, commerceProblem(contracts.InvalidState, "checkout reservation is missing")
	}
	if err != nil {
		return Payment{}, err
	}
	if reservationState != "active" || reservationQuantity != 1 || c.Qty != 1 || reservationExpiry != c.ExpiresAt.UnixMilli() || reservationExpiry <= now.UnixMilli() {
		return Payment{}, commerceProblem(contracts.InvalidState, "checkout reservation is inconsistent")
	}
	var leadState domain.LeadState
	if err = tx.QueryRowContext(ctx, `SELECT state FROM leads WHERE id=? AND seller_id=?`, c.LeadID, c.SellerID).Scan(&leadState); err != nil {
		return Payment{}, err
	}
	if leadState != domain.LeadReserved {
		return Payment{}, commerceProblem(contracts.InvalidState, "lead is not reserved for payment")
	}
	var physical int
	if err = tx.QueryRowContext(ctx, `SELECT physical_stock FROM inventory_items WHERE id=? AND seller_id=?`, c.ItemID, c.SellerID).Scan(&physical); err != nil {
		return Payment{}, err
	}
	held, err := s.ReservedStock(ctx, tx, c.SellerID, c.ItemID, now)
	if err != nil {
		return Payment{}, err
	}
	if physical < c.Qty || physical < held {
		return Payment{}, commerceProblem(contracts.OutOfStock, "reserved stock is no longer available")
	}
	receipt := Payment{ID: s.id(), CheckoutID: c.ID, LeadID: c.LeadID, AmountIDR: c.TotalIDR, Quantity: c.Qty, Currency: "IDR", PaidAt: now.UTC()}
	result, err := tx.ExecContext(ctx, `UPDATE inventory_items SET physical_stock=physical_stock-?,version=version+1,updated_at=? WHERE seller_id=? AND id=? AND physical_stock>=?`, c.Qty, now.UnixMilli(), c.SellerID, c.ItemID, c.Qty)
	if err != nil {
		return Payment{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Payment{}, commerceProblem(contracts.OutOfStock, "reserved stock is no longer available")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO stock_movements(id,seller_id,item_id,quantity_delta,quantity_after,kind,reason,created_at) VALUES(?,?,?,?,?,'payment',?,?)`, s.id(), c.SellerID, c.ItemID, -c.Qty, physical-c.Qty, "mock payment "+receipt.ID, now.UnixMilli())
	if err != nil {
		return Payment{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO payments(id,checkout_id,seller_id,lead_id,amount_idr,quantity,currency,idempotency_key,created_at) VALUES(?,?,?,?,?,?,'IDR',?,?)`, receipt.ID, c.ID, c.SellerID, c.LeadID, c.TotalIDR, c.Qty, actionKey, now.UnixMilli())
	if err != nil {
		return Payment{}, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE reservations SET status='committed',updated_at=? WHERE id=? AND status='active'`, now.UnixMilli(), c.ReservationID)
	if err != nil {
		return Payment{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Payment{}, commerceProblem(contracts.InvalidState, "reservation changed during payment")
	}
	result, err = tx.ExecContext(ctx, `UPDATE checkouts SET status='paid',updated_at=? WHERE id=? AND status='pending'`, now.UnixMilli(), c.ID)
	if err != nil {
		return Payment{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Payment{}, commerceProblem(contracts.InvalidState, "checkout changed during payment")
	}
	result, err = tx.ExecContext(ctx, `UPDATE leads SET state='paid',version=version+1,updated_at=? WHERE id=? AND seller_id=? AND state='reserved'`, now.UnixMilli(), c.LeadID, c.SellerID)
	if err != nil {
		return Payment{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Payment{}, commerceProblem(contracts.InvalidState, "lead changed during payment")
	}
	raw, err = json.Marshal(receipt)
	if err != nil {
		return Payment{}, err
	}
	if err = s.saveAction(ctx, tx, c.SellerID, actionKey, "pay_checkout", request, raw); err != nil {
		return Payment{}, err
	}
	return receipt, tx.Commit()
}
