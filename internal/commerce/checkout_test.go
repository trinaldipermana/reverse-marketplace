package commerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

type checkoutFixture struct {
	s     *Service
	db    *sql.DB
	clock atomic.Int64
}

func checkoutSetup(t *testing.T) *checkoutFixture {
	t.Helper()
	ctx := context.Background()
	db, err := mockpos.OpenSQLite(ctx, filepath.Join(t.TempDir(), "checkout.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	fixture := &checkoutFixture{db: db}
	fixture.clock.Store(time.Date(2026, 11, 7, 4, 0, 0, 0, time.UTC).UnixMilli())
	s, err := New(db, Options{Now: func() time.Time { return time.UnixMilli(fixture.clock.Load()).UTC() }, CheckoutOrigin: "https://checkout.example"})
	if err != nil {
		t.Fatal(err)
	}
	fixture.s = s
	if err = s.Inventory().SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}
func checkoutLeadOffer(t *testing.T, f *checkoutFixture, leadID, offerID, itemID string, price int64) {
	t.Helper()
	now := f.clock.Load()
	_, err := f.db.Exec(`INSERT INTO leads(id,seller_id,platform,post_id,buyer_id,thread_id,item_id,state,intent_json,created_at,updated_at) VALUES(?,'seller_demo','mock',?,?,?,?,'active','{}',?,?)`, leadID, "post_"+leadID, "buyer_"+leadID, "thread_"+leadID, itemID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.db.Exec(`INSERT INTO offers(id,seller_id,lead_id,item_id,price_idr,qty,policy_version,item_version,created_at) VALUES(?,'seller_demo',?,?,?,1,1,1,?)`, offerID, leadID, itemID, price, now)
	if err != nil {
		t.Fatal(err)
	}
}
func checkoutCode(t *testing.T, err error, want contracts.Code) {
	t.Helper()
	var business *contracts.Error
	if !errors.As(err, &business) || business.Code != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}
func checkoutScalar(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func checkoutState(t *testing.T, db *sql.DB, leadID string) string {
	t.Helper()
	var state string
	if err := db.QueryRow(`SELECT state FROM leads WHERE id=?`, leadID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}
func checkoutToken(t *testing.T, c domain.Checkout) string {
	t.Helper()
	u, err := url.Parse(c.URL)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(u.Path, "/checkout/")
}

func TestCheckoutStoredTermsAndIdempotency(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead", "offer", "item_thinkpad", 5_600_000)
	c, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "checkout-key")
	if err != nil {
		t.Fatal(err)
	}
	if c.PriceIDR != 5_600_000 || c.TotalIDR != 5_600_000 || c.Qty != 1 || c.Status != domain.CheckoutPending || c.ExpiresAt.Sub(time.UnixMilli(f.clock.Load())) != 30*time.Minute {
		t.Fatalf("wrong stored terms: %+v", c)
	}
	if !strings.HasPrefix(c.URL, "https://checkout.example/checkout/") || len(checkoutToken(t, c)) != 43 {
		t.Fatalf("wrong owned URL: %s", c.URL)
	}
	again, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "checkout-key")
	if err != nil || again != c {
		t.Fatalf("same key did not replay exact result: %+v %v", again, err)
	}
	other, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "different-key")
	if err != nil || other != c {
		t.Fatalf("new key duplicated checkout: %+v %v", other, err)
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM reservations`); n != 1 {
		t.Fatalf("reservation count %d", n)
	}
	if n := checkoutScalar(t, f.db, `SELECT physical_stock FROM inventory_items WHERE id='item_thinkpad'`); n != 2 {
		t.Fatalf("checkout consumed stock %d", n)
	}
	item, err := f.s.Inventory().Get(context.Background(), "seller_demo", "item_thinkpad")
	if err != nil || item.ReservedStock != 1 || item.AvailableStock != 1 {
		t.Fatalf("POS ignores hold: %+v %v", item, err)
	}
	_, err = f.s.Inventory().Adjust(context.Background(), "seller_demo", "item_thinkpad", "pos-reduce", mockpos.AdjustStock{ExpectedVersion: 1, Delta: -2, Reason: "reduce held stock"})
	checkoutCode(t, err, contracts.InvalidState)
	_, err = f.s.CreateCheckout(context.Background(), "lead", "wrong-offer", "checkout-key")
	checkoutCode(t, err, contracts.IdempotencyConflict)
}

func TestCheckoutRejectsStalePolicyOfferAndOwnership(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead", "old", "item_thinkpad", 5_600_000)
	if _, err := f.db.Exec(`UPDATE seller_policies SET floor_price_idr=5700000 WHERE seller_id='seller_demo'`); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.CreateCheckout(context.Background(), "lead", "old", "floor")
	checkoutCode(t, err, contracts.PriceBelowMinimum)
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM reservations`); n != 0 {
		t.Fatal("rejected floor reserved stock")
	}
	if _, err = f.db.Exec(`UPDATE seller_policies SET floor_price_idr=5400000 WHERE seller_id='seller_demo'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO offers(id,seller_id,lead_id,item_id,price_idr,qty,policy_version,item_version,created_at) VALUES('new','seller_demo','lead','item_thinkpad',5800000,1,1,1,?)`, f.clock.Load()); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CreateCheckout(context.Background(), "lead", "old", "stale")
	checkoutCode(t, err, contracts.OfferSuperseded)
	checkoutLeadOffer(t, f, "other", "other-offer", "item_dell", 6_000_000)
	_, err = f.s.CreateCheckout(context.Background(), "lead", "other-offer", "cross-lead")
	checkoutCode(t, err, contracts.NotFound)
	if _, err = f.db.Exec(`UPDATE inventory_items SET active=0 WHERE id='item_thinkpad'`); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CreateCheckout(context.Background(), "lead", "new", "inactive")
	checkoutCode(t, err, contracts.InvalidState)
}

func TestCheckoutLastUnitContention(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead-a", "offer-a", "item_dell", 6_000_000)
	checkoutLeadOffer(t, f, "lead-b", "offer-b", "item_dell", 6_000_000)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, suffix := range []string{"a", "b"} {
		wg.Add(1)
		go func(suffix string) {
			defer wg.Done()
			_, err := f.s.CreateCheckout(context.Background(), "lead-"+suffix, "offer-"+suffix, "key-"+suffix)
			results <- err
		}(suffix)
	}
	wg.Wait()
	close(results)
	success, blocked := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			checkoutCode(t, err, contracts.OutOfStock)
			blocked++
		}
	}
	if success != 1 || blocked != 1 {
		t.Fatalf("success=%d blocked=%d", success, blocked)
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM reservations WHERE status='active'`); n != 1 {
		t.Fatalf("active holds %d", n)
	}
}

func TestPaymentCommitsExactlyOnceAndPreservesOtherHolds(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead-a", "offer-a", "item_thinkpad", 5_600_000)
	checkoutLeadOffer(t, f, "lead-b", "offer-b", "item_thinkpad", 5_700_000)
	a, err := f.s.CreateCheckout(context.Background(), "lead-a", "offer-a", "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CreateCheckout(context.Background(), "lead-b", "offer-b", "b"); err != nil {
		t.Fatal(err)
	}
	token := checkoutToken(t, a)
	first, err := f.s.PayCheckout(context.Background(), token, "pay")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.PayCheckout(context.Background(), token, "pay")
	if err != nil || again != first {
		t.Fatalf("same-key replay: %+v %v", again, err)
	}
	later, err := f.s.PayCheckout(context.Background(), token, "new-pay-key")
	if err != nil || later != first {
		t.Fatalf("new-key receipt changed: %+v %v", later, err)
	}
	if first.AmountIDR != 5_600_000 || first.Quantity != 1 {
		t.Fatalf("wrong receipt: %+v", first)
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM payments`); n != 1 {
		t.Fatalf("receipts %d", n)
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM stock_movements WHERE kind='payment'`); n != 1 {
		t.Fatalf("payment movements %d", n)
	}
	item, err := f.s.Inventory().Get(context.Background(), "seller_demo", "item_thinkpad")
	if err != nil || item.PhysicalStock != 1 || item.ReservedStock != 1 || item.AvailableStock != 0 {
		t.Fatalf("double-subtracted hold: %+v %v", item, err)
	}
	if state := checkoutState(t, f.db, "lead-a"); state != "paid" {
		t.Fatalf("lead state %s", state)
	}
	view, err := f.s.GetCheckout(context.Background(), token)
	if err != nil || view.Payment == nil || view.Payment.ID != first.ID {
		t.Fatalf("paid view: %+v %v", view, err)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "floor") {
		t.Fatalf("public checkout leaks secrets: %s", raw)
	}
}

func TestPaymentAtExpiryPersistsRelease(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead", "offer", "item_thinkpad", 5_600_000)
	c, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	token := checkoutToken(t, c)
	f.clock.Store(c.ExpiresAt.UnixMilli())
	_, err = f.s.PayCheckout(context.Background(), token, "late-pay")
	checkoutCode(t, err, contracts.CheckoutExpired)
	if state := checkoutState(t, f.db, "lead"); state != "active" {
		t.Fatalf("expiry rolled back state %s", state)
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM reservations WHERE status='active'`); n != 0 {
		t.Fatal("expiry rolled back release")
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM payments`); n != 0 {
		t.Fatal("late payment committed")
	}
	next, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "new-checkout")
	if err != nil || next.ID == c.ID {
		t.Fatalf("fresh checkout failed after expiry: %+v %v", next, err)
	}
	if _, err = f.s.GetCheckout(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if state := checkoutState(t, f.db, "lead"); state != "reserved" {
		t.Fatalf("old expiry reactivated newer hold: %s", state)
	}
}

func TestPayAndExpiryRaceHasOneWinner(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead", "offer", "item_dell", 6_000_000)
	c, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Store(c.ExpiresAt.UnixMilli())
	var wg sync.WaitGroup
	wg.Add(2)
	var payErr, expireErr error
	go func() { defer wg.Done(); _, payErr = f.s.PayCheckout(context.Background(), checkoutToken(t, c), "pay") }()
	go func() { defer wg.Done(); _, expireErr = f.s.ExpireCheckouts(context.Background()) }()
	wg.Wait()
	checkoutCode(t, payErr, contracts.CheckoutExpired)
	if expireErr != nil {
		t.Fatal(expireErr)
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM payments`); n != 0 {
		t.Fatal("expiry race consumed stock")
	}
	if n := checkoutScalar(t, f.db, `SELECT physical_stock FROM inventory_items WHERE id='item_dell'`); n != 1 {
		t.Fatalf("stock %d", n)
	}
	if state := checkoutState(t, f.db, "lead"); state != "active" {
		t.Fatalf("state %s", state)
	}
}

func TestPaymentFailureRollsBackAllEffects(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER fail_receipt BEFORE INSERT ON payments BEGIN SELECT RAISE(ABORT,'receipt fail'); END`,
		`CREATE TRIGGER fail_ledger BEFORE INSERT ON stock_movements WHEN NEW.kind='payment' BEGIN SELECT RAISE(ABORT,'ledger fail'); END`,
		`CREATE TRIGGER fail_action BEFORE INSERT ON commerce_actions WHEN NEW.operation='pay_checkout' BEGIN SELECT RAISE(ABORT,'action fail'); END`,
	} {
		t.Run(trigger, func(t *testing.T) {
			f := checkoutSetup(t)
			checkoutLeadOffer(t, f, "lead", "offer", "item_dell", 6_000_000)
			c, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "checkout")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			_, err = f.s.PayCheckout(context.Background(), checkoutToken(t, c), "pay")
			if err == nil {
				t.Fatal("expected injected failure")
			}
			if n := checkoutScalar(t, f.db, `SELECT physical_stock FROM inventory_items WHERE id='item_dell'`); n != 1 {
				t.Fatalf("stock partially consumed %d", n)
			}
			if n := checkoutScalar(t, f.db, `SELECT count(*) FROM reservations WHERE status='active'`); n != 1 {
				t.Fatal("hold partially committed")
			}
			if n := checkoutScalar(t, f.db, `SELECT count(*) FROM payments`); n != 0 {
				t.Fatal("receipt partially committed")
			}
			if n := checkoutScalar(t, f.db, `SELECT count(*) FROM stock_movements WHERE kind='payment'`); n != 0 {
				t.Fatal("ledger partially committed")
			}
			if state := checkoutState(t, f.db, "lead"); state != "reserved" {
				t.Fatalf("lead partially paid %s", state)
			}
		})
	}
}

func TestCancelIsScopedAndDoesNotReopenPaidLead(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead", "offer", "item_dell", 6_000_000)
	c, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CancelCheckout(context.Background(), "other", c.ID)
	checkoutCode(t, err, contracts.NotFound)
	cancelled, err := f.s.CancelCheckout(context.Background(), "seller_demo", c.ID)
	if err != nil || cancelled.Status != domain.CheckoutCancelled {
		t.Fatalf("cancel %+v %v", cancelled, err)
	}
	_, err = f.s.PayCheckout(context.Background(), checkoutToken(t, c), "pay")
	checkoutCode(t, err, contracts.InvalidState)
	if state := checkoutState(t, f.db, "lead"); state != "active" {
		t.Fatalf("cancel state %s", state)
	}
	if _, err = f.s.CancelCheckout(context.Background(), "seller_demo", c.ID); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentPaymentReturnsOneReceipt(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead", "offer", "item_dell", 6_000_000)
	c, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	token := checkoutToken(t, c)
	var wg sync.WaitGroup
	receipts := make(chan Payment, 2)
	failures := make(chan error, 2)
	for _, key := range []string{"pay-a", "pay-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			receipt, err := f.s.PayCheckout(context.Background(), token, key)
			receipts <- receipt
			failures <- err
		}(key)
	}
	wg.Wait()
	close(receipts)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for receipt := range receipts {
		if id == "" {
			id = receipt.ID
		} else if id != receipt.ID {
			t.Fatal("concurrent payment returned distinct receipts")
		}
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM payments`); n != 1 {
		t.Fatalf("payments %d", n)
	}
	if n := checkoutScalar(t, f.db, `SELECT physical_stock FROM inventory_items WHERE id='item_dell'`); n != 0 {
		t.Fatalf("physical stock %d", n)
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM reservations WHERE status='committed'`); n != 1 {
		t.Fatalf("committed holds %d", n)
	}
}

func TestPaymentKeyCannotBeReusedForAnotherCheckout(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead-a", "offer-a", "item_thinkpad", 5_600_000)
	checkoutLeadOffer(t, f, "lead-b", "offer-b", "item_thinkpad", 5_700_000)
	a, err := f.s.CreateCheckout(context.Background(), "lead-a", "offer-a", "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.s.CreateCheckout(context.Background(), "lead-b", "offer-b", "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.PayCheckout(context.Background(), checkoutToken(t, a), "pay"); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.PayCheckout(context.Background(), checkoutToken(t, b), "pay")
	checkoutCode(t, err, contracts.IdempotencyConflict)
	if state := checkoutState(t, f.db, "lead-b"); state != "reserved" {
		t.Fatalf("conflict changed lead %s", state)
	}
}

func TestCheckoutActionFailureRollsBackHoldAndLead(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead", "offer", "item_dell", 6_000_000)
	if _, err := f.db.Exec(`CREATE TRIGGER fail_checkout_action BEFORE INSERT ON commerce_actions WHEN NEW.operation='create_checkout' BEGIN SELECT RAISE(ABORT,'injected action failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "key"); err == nil {
		t.Fatal("expected failure")
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM reservations`); n != 0 {
		t.Fatal("hold committed without replay result")
	}
	if n := checkoutScalar(t, f.db, `SELECT count(*) FROM checkouts`); n != 0 {
		t.Fatal("checkout committed without replay result")
	}
	if state := checkoutState(t, f.db, "lead"); state != "active" {
		t.Fatalf("state %s", state)
	}
}

func TestExpiryReadAndCancelNeverReopenTerminalLead(t *testing.T) {
	for _, terminal := range []string{"paid", "lost"} {
		t.Run(terminal, func(t *testing.T) {
			f := checkoutSetup(t)
			checkoutLeadOffer(t, f, "lead", "offer", "item_dell", 6_000_000)
			c, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "key")
			if err != nil {
				t.Fatal(err)
			}
			// Defensive stale-record case: cleanup may release the leftover hold but
			// must not overwrite a terminal state written by another service path.
			if _, err = f.db.Exec(`UPDATE leads SET state=? WHERE id='lead'`, terminal); err != nil {
				t.Fatal(err)
			}
			f.clock.Store(c.ExpiresAt.UnixMilli())
			view, err := f.s.GetCheckout(context.Background(), checkoutToken(t, c))
			if err != nil || view.Status != domain.CheckoutExpired {
				t.Fatalf("expiry view %+v %v", view, err)
			}
			if state := checkoutState(t, f.db, "lead"); state != terminal {
				t.Fatalf("cleanup reopened %s as %s", terminal, state)
			}
		})
	}
}

func TestCheckoutReplayRejectsReleasedOrPaidCheckout(t *testing.T) {
	for _, ending := range []string{"expired", "paid", "cancelled"} {
		t.Run(ending, func(t *testing.T) {
			f := checkoutSetup(t)
			checkoutLeadOffer(t, f, "lead", "offer", "item_dell", 6_000_000)
			original, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "key")
			if err != nil {
				t.Fatal(err)
			}
			switch ending {
			case "expired":
				f.clock.Store(original.ExpiresAt.UnixMilli())
			case "paid":
				if _, err = f.s.PayCheckout(context.Background(), checkoutToken(t, original), "pay"); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				if _, err = f.s.CancelCheckout(context.Background(), "seller_demo", original.ID); err != nil {
					t.Fatal(err)
				}
			}
			_, err = f.s.CreateCheckout(context.Background(), "lead", "offer", "key")
			want := contracts.InvalidState
			if ending == "expired" {
				want = contracts.CheckoutExpired
			}
			checkoutCode(t, err, want)
			if n := checkoutScalar(t, f.db, `SELECT count(*) FROM reservations`); n != 1 {
				t.Fatal("replay created another reservation")
			}
			if n := checkoutScalar(t, f.db, `SELECT count(*) FROM reservations WHERE status='active'`); n != 0 {
				t.Fatal("released checkout claims a live hold")
			}
		})
	}
}
