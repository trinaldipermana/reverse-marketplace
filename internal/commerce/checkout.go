package commerce

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

type checkoutRecord struct {
	domain.Checkout
	SellerID      string
	ReservationID string
	Token         string
}

// checkoutReplay explicitly preserves the delivery URL in the restricted action
// result. domain.Checkout deliberately excludes that field from normal JSON.
type checkoutReplay struct {
	Checkout domain.Checkout
	URL      string
}

func checkoutResult(c domain.Checkout) json.RawMessage {
	raw, _ := json.Marshal(checkoutReplay{Checkout: c, URL: c.URL})
	return raw
}
func checkoutFromResult(raw json.RawMessage) (domain.Checkout, error) {
	var saved checkoutReplay
	if err := json.Unmarshal(raw, &saved); err != nil {
		return domain.Checkout{}, err
	}
	saved.Checkout.URL = saved.URL
	return saved.Checkout, nil
}

func checkoutTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (s *Service) checkoutURL(token string) string {
	return s.checkoutOrigin + "/checkout/" + url.PathEscape(token)
}

const checkoutColumns = `id,seller_id,lead_id,offer_id,reservation_id,item_id,unit_price_idr,quantity,total_idr,status,expires_at,token`

func (s *Service) scanCheckout(row interface{ Scan(...any) error }) (checkoutRecord, error) {
	var c checkoutRecord
	var expires int64
	err := row.Scan(&c.ID, &c.SellerID, &c.LeadID, &c.OfferID, &c.ReservationID, &c.ItemID, &c.PriceIDR, &c.Qty, &c.TotalIDR, &c.Status, &expires, &c.Token)
	if errors.Is(err, sql.ErrNoRows) {
		return c, commerceProblem(contracts.NotFound, "checkout not found")
	}
	if err != nil {
		return c, err
	}
	c.ExpiresAt = time.UnixMilli(expires).UTC()
	c.URL = s.checkoutURL(c.Token)
	return c, nil
}
func (s *Service) checkoutByTokenTx(ctx context.Context, tx *sql.Tx, token string) (checkoutRecord, error) {
	if len(token) != 43 {
		return checkoutRecord{}, commerceProblem(contracts.NotFound, "checkout not found")
	}
	return s.scanCheckout(tx.QueryRowContext(ctx, `SELECT `+checkoutColumns+` FROM checkouts WHERE token_hash=?`, checkoutTokenHash(token)))
}
func (s *Service) checkoutByIDTx(ctx context.Context, tx *sql.Tx, id string) (checkoutRecord, error) {
	return s.scanCheckout(tx.QueryRowContext(ctx, `SELECT `+checkoutColumns+` FROM checkouts WHERE id=?`, id))
}

// CreateCheckout atomically validates a stored offer, reserves one unit for 30
// minutes, and changes the lead to reserved. No buyer message is sent here.
func (s *Service) CreateCheckout(ctx context.Context, leadID, offerID, actionKey string) (domain.Checkout, error) {
	if strings.TrimSpace(leadID) == "" || strings.TrimSpace(offerID) == "" {
		return domain.Checkout{}, commerceProblem(contracts.InvalidArgument, "lead and offer are required")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return domain.Checkout{}, err
	}
	defer tx.Rollback()
	var seller string
	if err = tx.QueryRowContext(ctx, `SELECT seller_id FROM leads WHERE id=?`, leadID).Scan(&seller); errors.Is(err, sql.ErrNoRows) {
		return domain.Checkout{}, commerceProblem(contracts.NotFound, "lead not found")
	} else if err != nil {
		return domain.Checkout{}, err
	}
	request := struct{ LeadID, OfferID string }{leadID, offerID}
	raw, found, err := replayAction(ctx, tx, seller, actionKey, "create_checkout", request)
	if err != nil {
		return domain.Checkout{}, err
	}
	if found {
		saved, err := checkoutFromResult(raw)
		if err != nil {
			return domain.Checkout{}, err
		}
		// A live replay preserves identity/URL. A released/paid checkout returns
		// a business rejection rather than stale pending data or another hold.
		current, err := s.checkoutByIDTx(ctx, tx, saved.ID)
		if err != nil {
			return domain.Checkout{}, err
		}
		if current.SellerID != seller || current.LeadID != leadID || current.OfferID != offerID {
			return domain.Checkout{}, commerceProblem(contracts.InvalidState, "stored checkout action is inconsistent")
		}
		if current.Status == domain.CheckoutPending && !s.now().Before(current.ExpiresAt) {
			if _, err = s.releaseCheckoutTx(ctx, tx, current, "expired", s.now()); err != nil {
				return domain.Checkout{}, err
			}
			if err = tx.Commit(); err != nil {
				return domain.Checkout{}, err
			}
			return domain.Checkout{}, commerceProblem(contracts.CheckoutExpired, "checkout has expired")
		}
		if current.Status == domain.CheckoutExpired {
			return domain.Checkout{}, commerceProblem(contracts.CheckoutExpired, "checkout has expired")
		}
		if current.Status != domain.CheckoutPending {
			return domain.Checkout{}, commerceProblem(contracts.InvalidState, "checkout is no longer pending")
		}
		current.URL = saved.URL
		return current.Checkout, tx.Commit()
	}
	now := time.UnixMilli(s.now().UnixMilli()).UTC()
	if _, err = s.expireCheckoutsTx(ctx, tx, now); err != nil {
		return domain.Checkout{}, err
	}
	var state domain.LeadState
	var leadItem, storedItem string
	var price int64
	var quantity int
	err = tx.QueryRowContext(ctx, `SELECT l.state,l.item_id,o.item_id,o.price_idr,o.qty FROM leads l JOIN offers o ON o.lead_id=l.id AND o.seller_id=l.seller_id WHERE l.id=? AND o.id=?`, leadID, offerID).Scan(&state, &leadItem, &storedItem, &price, &quantity)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Checkout{}, commerceProblem(contracts.NotFound, "offer not found for lead")
	}
	if err != nil {
		return domain.Checkout{}, err
	}
	var currentOffer string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM offers WHERE lead_id=? ORDER BY seq DESC LIMIT 1`, leadID).Scan(&currentOffer); err != nil {
		return domain.Checkout{}, err
	}
	if currentOffer != offerID {
		return domain.Checkout{}, commerceProblem(contracts.OfferSuperseded, "offer has been superseded")
	}
	if leadItem != storedItem || quantity != 1 || price <= 0 {
		return domain.Checkout{}, commerceProblem(contracts.InvalidState, "stored offer terms are inconsistent")
	}
	if state == domain.LeadReserved {
		existing, err := s.scanCheckout(tx.QueryRowContext(ctx, `SELECT `+checkoutColumns+` FROM checkouts WHERE lead_id=? AND status='pending'`, leadID))
		if err != nil {
			return domain.Checkout{}, err
		}
		if existing.OfferID != offerID || existing.ItemID != storedItem || existing.PriceIDR != price || existing.Qty != quantity {
			return domain.Checkout{}, commerceProblem(contracts.InvalidState, "lead already has a different live checkout")
		}
		if err = s.saveAction(ctx, tx, seller, actionKey, "create_checkout", request, checkoutResult(existing.Checkout)); err != nil {
			return domain.Checkout{}, err
		}
		return existing.Checkout, tx.Commit()
	}
	if state != domain.LeadActive {
		return domain.Checkout{}, commerceProblem(contracts.InvalidState, "lead is not active for checkout")
	}
	item, err := s.itemTx(ctx, tx, seller, storedItem)
	if err != nil {
		return domain.Checkout{}, err
	}
	if !item.Active {
		return domain.Checkout{}, commerceProblem(contracts.InvalidState, "item is not active")
	}
	if item.AvailableStock < quantity {
		return domain.Checkout{}, commerceProblem(contracts.OutOfStock, "item is no longer available")
	}
	rules, err := effectiveRulesTx(ctx, tx, seller, storedItem)
	if err != nil {
		return domain.Checkout{}, err
	}
	minimum, err := effectiveMinimum(item.ListPriceIDR, rules)
	if err != nil {
		return domain.Checkout{}, err
	}
	if price < minimum {
		return domain.Checkout{}, commerceProblem(contracts.PriceBelowMinimum, "offer is below the seller's minimum")
	}
	var tokenBytes [32]byte
	if _, err = rand.Read(tokenBytes[:]); err != nil {
		return domain.Checkout{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	expires := now.Add(30 * time.Minute)
	reservationID := s.id()
	checkoutID := s.id()
	_, err = tx.ExecContext(ctx, `INSERT INTO reservations(id,seller_id,lead_id,item_id,quantity,status,expires_at,released_reason,created_at,updated_at) VALUES(?,?,?,?,?,'active',?,'',?,?)`, reservationID, seller, leadID, storedItem, quantity, expires.UnixMilli(), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return domain.Checkout{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO checkouts(id,seller_id,lead_id,offer_id,reservation_id,item_id,unit_price_idr,quantity,total_idr,status,expires_at,token_hash,token,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'pending',?,?,?,?,?)`, checkoutID, seller, leadID, offerID, reservationID, storedItem, price, quantity, price*int64(quantity), expires.UnixMilli(), checkoutTokenHash(token), token, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return domain.Checkout{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE leads SET state='reserved',version=version+1,updated_at=? WHERE id=? AND seller_id=? AND state='active'`, now.UnixMilli(), leadID, seller)
	if err != nil {
		return domain.Checkout{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return domain.Checkout{}, commerceProblem(contracts.InvalidState, "lead changed before checkout")
	}
	out := domain.Checkout{ID: checkoutID, LeadID: leadID, OfferID: offerID, ItemID: storedItem, PriceIDR: price, Qty: quantity, TotalIDR: price * int64(quantity), URL: s.checkoutURL(token), Status: domain.CheckoutPending, ExpiresAt: expires.UTC()}
	if err = s.saveAction(ctx, tx, seller, actionKey, "create_checkout", request, checkoutResult(out)); err != nil {
		return domain.Checkout{}, err
	}
	return out, tx.Commit()
}

func (s *Service) GetCheckout(ctx context.Context, token string) (CheckoutView, error) {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return CheckoutView{}, err
	}
	defer tx.Rollback()
	c, err := s.checkoutByTokenTx(ctx, tx, token)
	if err != nil {
		return CheckoutView{}, err
	}
	if c.Status == domain.CheckoutPending && !s.now().Before(c.ExpiresAt) {
		if _, err = s.releaseCheckoutTx(ctx, tx, c, "expired", s.now()); err != nil {
			return CheckoutView{}, err
		}
		c.Status = domain.CheckoutExpired
	}
	var name string
	if err = tx.QueryRowContext(ctx, `SELECT name FROM inventory_items WHERE seller_id=? AND id=?`, c.SellerID, c.ItemID).Scan(&name); err != nil {
		return CheckoutView{}, err
	}
	view := CheckoutView{ID: c.ID, LeadID: c.LeadID, ItemID: c.ItemID, ItemName: name, PriceIDR: c.PriceIDR, Quantity: c.Qty, TotalIDR: c.TotalIDR, Currency: "IDR", Status: c.Status, ExpiresAt: c.ExpiresAt}
	if c.Status == domain.CheckoutPaid {
		receipt, err := paymentForCheckoutTx(ctx, tx, c.ID)
		if err != nil {
			return CheckoutView{}, err
		}
		view.Payment = &receipt
	}
	return view, tx.Commit()
}
