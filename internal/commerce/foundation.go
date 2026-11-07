package commerce

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// Options supplies shared transaction context. No service opens its own DB.
type Options struct {
	Now            func() time.Time
	NewID          func() string
	CheckoutOrigin string
}
type Service struct {
	db             *sql.DB
	now            func() time.Time
	id             func() string
	checkoutOrigin string
	inventory      *mockpos.Service
}

func New(db *sql.DB, opts Options) (*Service, error) {
	if db == nil {
		return nil, commerceProblem(contracts.InvalidArgument, "database is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = func() string {
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				panic(err)
			}
			return hex.EncodeToString(b[:])
		}
	}
	if opts.CheckoutOrigin == "" {
		opts.CheckoutOrigin = "http://127.0.0.1:8080"
	}
	origin, err := url.Parse(opts.CheckoutOrigin)
	if err != nil || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return nil, commerceProblem(contracts.InvalidArgument, "checkout origin must be an HTTP origin without path or credentials")
	}
	s := &Service{db: db, now: opts.Now, id: opts.NewID, checkoutOrigin: strings.TrimRight(opts.CheckoutOrigin, "/")}
	s.inventory, err = mockpos.New(db, mockpos.Options{Now: s.now, NewID: s.id, ReservedStock: s.ReservedStock})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Inventory is the POS service wired to authoritative reservation SQL.
func (s *Service) Inventory() *mockpos.Service { return s.inventory }

func commerceProblem(code contracts.Code, message string) error {
	return &contracts.Error{Code: code, Message: message}
}

// beginWrite acquires SQLite's write lock before any lead/stock reads, including
// when the caller injected a connection without the driver's immediate option.
func (s *Service) beginWrite(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sellers SET name=name WHERE id=(SELECT id FROM sellers ORDER BY id LIMIT 1)`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// ReservedStock is compatible with mockpos.Options.ReservedStock. Query using
// the caller's transaction so allocation and POS adjustment see the same state.
func (s *Service) ReservedStock(ctx context.Context, tx *sql.Tx, seller, item string, now time.Time) (int, error) {
	var quantity int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(quantity),0) FROM reservations WHERE seller_id=? AND item_id=? AND status='active' AND expires_at>?`, seller, item, now.UnixMilli()).Scan(&quantity)
	return quantity, err
}

func (s *Service) itemTx(ctx context.Context, tx *sql.Tx, seller, item string) (domain.Item, error) {
	var out domain.Item
	var attrs string
	var stock int
	err := tx.QueryRowContext(ctx, `SELECT id,seller_id,name,description,attributes_json,list_price_idr,physical_stock,city,active,version FROM inventory_items WHERE seller_id=? AND id=?`, seller, item).Scan(&out.ID, &out.SellerID, &out.Name, &out.Description, &attrs, &out.ListPriceIDR, &stock, &out.Location.City, &out.Active, &out.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return out, commerceProblem(contracts.NotFound, "item not found")
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(attrs), &out.Attributes); err != nil {
		return out, err
	}
	held, err := s.ReservedStock(ctx, tx, seller, item, s.now())
	if err != nil {
		return out, err
	}
	if held < 0 || held > stock {
		return out, commerceProblem(contracts.InvalidState, "inconsistent inventory allocation")
	}
	out.AvailableStock = stock - held
	return out, nil
}

func effectiveRulesTx(ctx context.Context, tx *sql.Tx, seller, item string) (domain.Rules, error) {
	var out domain.Rules
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(o.floor_price_idr,p.floor_price_idr),COALESCE(o.max_discount_bps,p.max_discount_bps),p.message_cap,p.rate_limit_per_minute,p.version+COALESCE(o.version,0) FROM inventory_items i JOIN seller_policies p ON p.seller_id=i.seller_id LEFT JOIN item_policy_overrides o ON o.item_id=i.id AND o.seller_id=i.seller_id WHERE i.seller_id=? AND i.id=?`, seller, item).Scan(&out.FloorPriceIDR, &out.MaxDiscountBPS, &out.MessageCap, &out.RateLimitPerMinute, &out.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return out, commerceProblem(contracts.NotFound, "item policy not found")
	}
	return out, err
}

// checkoutForLead is an internal projection; bearer URL never goes to logs or
// normal lead JSON. A completed/expired checkout remains visible as history.
func (s *Service) checkoutForLead(ctx context.Context, tx *sql.Tx, lead string) (*domain.Checkout, error) {
	var out domain.Checkout
	var token string
	var expiry int64
	err := tx.QueryRowContext(ctx, `SELECT id,lead_id,offer_id,item_id,unit_price_idr,quantity,total_idr,status,expires_at,token FROM checkouts WHERE lead_id=? ORDER BY seq DESC LIMIT 1`, lead).Scan(&out.ID, &out.LeadID, &out.OfferID, &out.ItemID, &out.PriceIDR, &out.Qty, &out.TotalIDR, &out.Status, &expiry, &token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out.ExpiresAt = time.UnixMilli(expiry).UTC()
	out.URL = s.checkoutOrigin + "/checkout/" + url.PathEscape(token)
	return &out, nil
}

func actionFingerprint(operation string, request any) (string, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(operation+"\n"), raw...))
	return hex.EncodeToString(sum[:]), nil
}
func replayAction(ctx context.Context, tx *sql.Tx, seller, key, operation string, request any) (json.RawMessage, bool, error) {
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		return nil, false, commerceProblem(contracts.InvalidArgument, "action key is required and must be at most 200 characters")
	}
	hash, err := actionFingerprint(operation, request)
	if err != nil {
		return nil, false, err
	}
	var op, previous, result string
	err = tx.QueryRowContext(ctx, `SELECT operation,request_hash,result_json FROM commerce_actions WHERE seller_id=? AND action_key=?`, seller, key).Scan(&op, &previous, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if op != operation || previous != hash {
		return nil, false, commerceProblem(contracts.IdempotencyConflict, "action key already used for different arguments")
	}
	return json.RawMessage(result), true, nil
}
func (s *Service) saveAction(ctx context.Context, tx *sql.Tx, seller, key, operation string, request any, result json.RawMessage) error {
	if !json.Valid(result) {
		return errors.New("invalid internal action result")
	}
	hash, err := actionFingerprint(operation, request)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO commerce_actions(seller_id,action_key,operation,request_hash,result_json,created_at) VALUES(?,?,?,?,?,?)`, seller, key, operation, hash, string(result), s.now().UnixMilli())
	return err
}
