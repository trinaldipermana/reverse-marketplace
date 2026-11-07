package commerce

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

func phase3Fixture(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := mockpos.OpenSQLite(ctx, filepath.Join(t.TempDir(), "commerce.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	s, err := New(db, Options{Now: func() time.Time { return now }, CheckoutOrigin: "http://localhost:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Inventory().SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	return s, db
}
func phase3Input(post string) CreateLeadInput {
	budget := int64(6_000_000)
	return CreateLeadInput{SellerID: mockpos.DemoSellerID, Platform: "mock", PostID: post, BuyerID: "buyer_" + post, ThreadID: "thread_" + post, ItemID: "item_thinkpad",
		Intent: domain.Intent{IsBuyingRequest: true, Confidence: 0.96, ItemQuery: "used coding laptop", Qty: 1, Location: domain.Location{City: "Jakarta"}, BudgetIDR: &budget}, MatchReason: "16GB RAM and suitable price"}
}
func phase3Lead(t *testing.T, s *Service, post string) domain.LeadRecord {
	t.Helper()
	lead, created, err := s.CreateLead(context.Background(), phase3Input(post))
	if err != nil || !created {
		t.Fatalf("create lead: %+v created=%v err=%v", lead, created, err)
	}
	return lead
}
func phase3Code(t *testing.T, err error, want contracts.Code) {
	t.Helper()
	var failure *contracts.Error
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}
func phase3Count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateLeadDeduplicatesAndKeepsTerminalState(t *testing.T) {
	s, db := phase3Fixture(t)
	ctx := context.Background()
	in := phase3Input("post-a")
	in.AlternativeItemIDs = []string{"item_dell", "item_dell", "item_thinkpad"}
	lead, created, err := s.CreateLead(ctx, in)
	if err != nil || !created {
		t.Fatalf("create: %+v %v %v", lead, created, err)
	}
	if lead.Platform != in.Platform || lead.PostID != in.PostID || lead.BuyerID != in.BuyerID || lead.ThreadID != in.ThreadID || lead.State != domain.LeadActive || lead.Offer != nil || lead.Checkout != nil {
		t.Fatalf("identity/state lost: %+v", lead)
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM runtime_notifications WHERE lead_id=? AND status='pending'`, lead.ID); n != 1 {
		t.Fatalf("notification intents=%d", n)
	}
	var alternatives string
	if err = db.QueryRow(`SELECT alternative_item_ids_json FROM leads WHERE id=?`, lead.ID).Scan(&alternatives); err != nil || alternatives != `["item_dell"]` {
		t.Fatalf("alternatives=%s err=%v", alternatives, err)
	}
	closed, err := s.TransitionLead(ctx, lead.ID, domain.LeadLost, "buyer declined")
	if err != nil {
		t.Fatal(err)
	}
	in.ItemID = "item_dell"
	again, created, err := s.CreateLead(ctx, in)
	if err != nil || created || again.ID != lead.ID || again.ItemID != lead.ItemID || again.State != domain.LeadLost || again.Version != closed.Version {
		t.Fatalf("existing lead changed: %+v created=%v err=%v", again, created, err)
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM runtime_notifications`); n != 1 {
		t.Fatalf("duplicate notification: %d", n)
	}
}

func TestConcurrentLeadCreationCreatesOneNotification(t *testing.T) {
	s, db := phase3Fixture(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	createdCount := 0
	ids := map[string]bool{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lead, created, err := s.CreateLead(context.Background(), phase3Input("same-post"))
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids[lead.ID] = true
			if created {
				createdCount++
			}
		}()
	}
	wg.Wait()
	if createdCount != 1 || len(ids) != 1 {
		t.Fatalf("created=%d identities=%d", createdCount, len(ids))
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM leads`); n != 1 {
		t.Fatalf("lead count=%d", n)
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM runtime_notifications`); n != 1 {
		t.Fatalf("notification count=%d", n)
	}
}

func TestCreateLeadNotificationFailureRollsBackLead(t *testing.T) {
	s, db := phase3Fixture(t)
	if _, err := db.Exec(`CREATE TRIGGER fail_notification BEFORE INSERT ON runtime_notifications BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CreateLead(context.Background(), phase3Input("rollback"))
	if err == nil {
		t.Fatal("expected notification failure")
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM leads`); n != 0 {
		t.Fatal("lead persisted without notification intent")
	}
}

func TestCreateLeadAndReadsEnforceSellerAndInventory(t *testing.T) {
	s, db := phase3Fixture(t)
	ctx := context.Background()
	in := phase3Input("bad-scope")
	in.SellerID = "unknown"
	_, _, err := s.CreateLead(ctx, in)
	phase3Code(t, err, contracts.NotFound)
	in = phase3Input("inactive")
	if _, err = db.Exec(`UPDATE inventory_items SET active=0 WHERE id='item_thinkpad'`); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CreateLead(ctx, in)
	phase3Code(t, err, contracts.InvalidState)
	if _, err = db.Exec(`UPDATE inventory_items SET active=1,physical_stock=0 WHERE id='item_thinkpad'`); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CreateLead(ctx, in)
	phase3Code(t, err, contracts.OutOfStock)
	if _, err = db.Exec(`UPDATE inventory_items SET physical_stock=2 WHERE id='item_thinkpad'`); err != nil {
		t.Fatal(err)
	}
	lead := phase3Lead(t, s, "good")
	_, err = s.GetSellerLead(ctx, "other", lead.ID)
	phase3Code(t, err, contracts.NotFound)
	_, err = s.GetItem(ctx, "other", lead.ItemID)
	phase3Code(t, err, contracts.NotFound)
	_, err = s.GetRules(ctx, "other", lead.ItemID)
	phase3Code(t, err, contracts.NotFound)
	_, err = s.GetLead(ctx, "missing")
	phase3Code(t, err, contracts.NotFound)
}

func TestRecordOfferValidBlockedAndIdempotent(t *testing.T) {
	s, db := phase3Fixture(t)
	ctx := context.Background()
	lead := phase3Lead(t, s, "offer")
	_, err := s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_399_999, 1, "below")
	phase3Code(t, err, contracts.PriceBelowMinimum)
	if strings.Contains(err.Error(), "5400000") || strings.Contains(err.Error(), "5399999") {
		t.Fatal("private rule or proposal leaked into rejection")
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM offers`); n != 0 {
		t.Fatal("blocked offer persisted")
	}
	offer, err := s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_400_000, 1, "valid")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_400_000, 1, "valid")
	if err != nil || again != offer {
		t.Fatalf("replay changed offer: %+v %v", again, err)
	}
	_, err = s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_500_000, 1, "valid")
	phase3Code(t, err, contracts.IdempotencyConflict)
	_, err = s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_800_000, 2, "two-units")
	phase3Code(t, err, contracts.InvalidArgument)
	if n := phase3Count(t, db, `SELECT count(*) FROM offers`); n != 1 {
		t.Fatalf("offer count=%d", n)
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM reservations`); n != 0 {
		t.Fatal("offer reserved stock")
	}
	loaded, err := s.GetLead(ctx, lead.ID)
	if err != nil || loaded.Offer == nil || *loaded.Offer != offer {
		t.Fatalf("current offer missing: %+v %v", loaded, err)
	}
	if loaded.State != domain.LeadActive || loaded.Version != 2 {
		t.Fatalf("offer state/version=%+v", loaded)
	}
}

func TestLatestOfferOrderingAndItemSwitchMatchP1Context(t *testing.T) {
	s, _ := phase3Fixture(t)
	ctx := context.Background()
	lead := phase3Lead(t, s, "switch")
	first, err := s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_600_000, 1, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RecordOffer(ctx, lead.ID, "item_dell", 5_700_000, 1, "second")
	if err != nil {
		t.Fatal(err)
	}
	if !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatal("fixture should exercise equal timestamps")
	}
	loaded, err := s.GetLead(ctx, lead.ID)
	if err != nil || loaded.ItemID != "item_dell" || loaded.Offer == nil || loaded.Offer.ID != second.ID {
		t.Fatalf("latest offer/item inconsistent: %+v %v", loaded, err)
	}
	context, err := runtime.LoadLeadContext(ctx, s, lead.ID)
	if err != nil || context.Item.ID != second.ItemID {
		t.Fatalf("P1 context rejected current offer: %+v %v", context, err)
	}
}

func TestRulesOverridesAndCurrentStockAreAuthoritative(t *testing.T) {
	s, db := phase3Fixture(t)
	ctx := context.Background()
	lead := phase3Lead(t, s, "rules")
	if _, err := db.Exec(`INSERT INTO item_policy_overrides(item_id,seller_id,floor_price_idr,max_discount_bps,version,updated_at) VALUES('item_thinkpad',?,NULL,0,1,0)`, mockpos.DemoSellerID); err != nil {
		t.Fatal(err)
	}
	rules, err := s.GetRules(ctx, mockpos.DemoSellerID, "item_thinkpad")
	if err != nil || rules.FloorPriceIDR != 5_400_000 || rules.MaxDiscountBPS != 0 {
		t.Fatalf("effective policy: %+v %v", rules, err)
	}
	_, err = s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_700_000, 1, "override")
	phase3Code(t, err, contracts.PriceBelowMinimum)
	if _, err = db.Exec(`UPDATE inventory_items SET physical_stock=0 WHERE id='item_thinkpad'`); err != nil {
		t.Fatal(err)
	}
	_, err = s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_800_000, 1, "no-stock")
	phase3Code(t, err, contracts.OutOfStock)
	if _, err = db.Exec(`UPDATE inventory_items SET physical_stock=2,active=0 WHERE id='item_thinkpad'`); err != nil {
		t.Fatal(err)
	}
	_, err = s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_800_000, 1, "inactive")
	phase3Code(t, err, contracts.InvalidState)
}

func TestOfferFailureRollsBackCurrentItemAndAction(t *testing.T) {
	s, db := phase3Fixture(t)
	ctx := context.Background()
	lead := phase3Lead(t, s, "rollback-offer")
	if _, err := db.Exec(`CREATE TRIGGER fail_commerce_action BEFORE INSERT ON commerce_actions BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.RecordOffer(ctx, lead.ID, "item_dell", 5_700_000, 1, "failure")
	if err == nil {
		t.Fatal("expected action failure")
	}
	loaded, err := s.GetLead(ctx, lead.ID)
	if err != nil || loaded.ItemID != lead.ItemID || loaded.Offer != nil || loaded.Version != 1 {
		t.Fatalf("offer partially committed: %+v %v", loaded, err)
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM offers`); n != 0 {
		t.Fatal("offer persisted without action result")
	}
}

func TestTransitionsAreActiveOnlyAndRepeatSafely(t *testing.T) {
	s, _ := phase3Fixture(t)
	ctx := context.Background()
	lead := phase3Lead(t, s, "transition")
	for _, target := range []domain.LeadState{domain.LeadPaid, domain.LeadReserved, domain.LeadActive} {
		_, err := s.TransitionLead(ctx, lead.ID, target, "bypass")
		phase3Code(t, err, contracts.InvalidArgument)
	}
	escalated, err := s.TransitionLead(ctx, lead.ID, domain.LeadEscalated, "warranty unknown")
	if err != nil || escalated.State != domain.LeadEscalated || escalated.Version != 2 {
		t.Fatalf("escalate: %+v %v", escalated, err)
	}
	again, err := s.TransitionLead(ctx, lead.ID, domain.LeadEscalated, "warranty unknown")
	if err != nil || again.Version != escalated.Version {
		t.Fatalf("retry mutated transition: %+v %v", again, err)
	}
	_, err = s.TransitionLead(ctx, lead.ID, domain.LeadEscalated, "another reason")
	phase3Code(t, err, contracts.InvalidState)
	_, err = s.TransitionLead(ctx, lead.ID, domain.LeadLost, "close after escalation")
	phase3Code(t, err, contracts.InvalidState)
	_, err = s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_800_000, 1, "after-escalation")
	phase3Code(t, err, contracts.InvalidState)
	closed := phase3Lead(t, s, "closed")
	_, err = s.TransitionLead(ctx, closed.ID, domain.LeadLost, "buyer declined")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.TransitionLead(ctx, closed.ID, domain.LeadEscalated, "reopen")
	phase3Code(t, err, contracts.InvalidState)
}

func TestConcurrentSameOfferKeyIsExactlyOnce(t *testing.T) {
	s, db := phase3Fixture(t)
	lead := phase3Lead(t, s, "parallel-offer")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[string]bool{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			offer, err := s.RecordOffer(context.Background(), lead.ID, lead.ItemID, 5_600_000, 1, "same-key")
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids[offer.ID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != 1 {
		t.Fatalf("offer identities=%d", len(ids))
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM offers`); n != 1 {
		t.Fatalf("offers=%d", n)
	}
	loaded, err := s.GetLead(context.Background(), lead.ID)
	if err != nil || loaded.Version != 2 {
		t.Fatalf("replay updated lead repeatedly: %+v %v", loaded, err)
	}
}

func TestPhase3CancellationBeforeWrites(t *testing.T) {
	s, db := phase3Fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := s.CreateLead(ctx, phase3Input("cancel"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM leads`); n != 0 {
		t.Fatal("cancelled create wrote a lead")
	}
}

func TestOfferRejectsAnotherSellersItem(t *testing.T) {
	s, db := phase3Fixture(t)
	ctx := context.Background()
	lead := phase3Lead(t, s, "seller-scope")
	if _, err := db.Exec(`INSERT INTO sellers(id,name,city,created_at) VALUES('other','Other','Jakarta',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO seller_policies(seller_id,floor_price_idr,max_discount_bps,message_cap,rate_limit_per_minute,version,updated_at) VALUES('other',0,1000,10,20,1,0)`); err != nil {
		t.Fatal(err)
	}
	item, err := s.Inventory().Create(ctx, "other", "other-item", mockpos.CreateItem{Name: "Other laptop", ListPriceIDR: 6_000_000, InitialStock: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RecordOffer(ctx, lead.ID, item.ID, 6_000_000, 1, "cross-seller")
	phase3Code(t, err, contracts.NotFound)
	if n := phase3Count(t, db, `SELECT count(*) FROM offers`); n != 0 {
		t.Fatal("cross-seller offer persisted")
	}
}

func TestOfferAvailabilityExcludesUnexpiredReservations(t *testing.T) {
	s, db := phase3Fixture(t)
	ctx := context.Background()
	lead := phase3Lead(t, s, "target")
	holdA := phase3Lead(t, s, "hold-a")
	holdB := phase3Lead(t, s, "hold-b")
	now := s.now().UnixMilli()
	for _, holder := range []domain.LeadRecord{holdA, holdB} {
		_, err := db.Exec(`INSERT INTO reservations(id,seller_id,lead_id,item_id,quantity,status,expires_at,created_at,updated_at) VALUES(?,?,?,?,1,'active',?,?,?)`, s.id(), holder.SellerID, holder.ID, holder.ItemID, now+60000, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_800_000, 1, "held-out")
	phase3Code(t, err, contracts.OutOfStock)
	if _, err = db.Exec(`UPDATE reservations SET expires_at=?`, now); err != nil {
		t.Fatal(err)
	}
	offer, err := s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_800_000, 1, "after-expiry")
	if err != nil || offer.ID == "" {
		t.Fatalf("expired reservations blocked offer: %+v %v", offer, err)
	}
}

func TestOfferReplayRemainsReadOnlyAfterLeadCloses(t *testing.T) {
	s, db := phase3Fixture(t)
	ctx := context.Background()
	lead := phase3Lead(t, s, "retry-terminal")
	offer, err := s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_600_000, 1, "offer")
	if err != nil {
		t.Fatal(err)
	}
	closed, err := s.TransitionLead(ctx, lead.ID, domain.LeadLost, "buyer declined")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_600_000, 1, "offer")
	if err != nil || again != offer {
		t.Fatalf("safe read-only replay rejected: %+v %v", again, err)
	}
	loaded, err := s.GetLead(ctx, lead.ID)
	if err != nil || loaded.State != domain.LeadLost || loaded.Version != closed.Version {
		t.Fatalf("replay reopened lead: %+v %v", loaded, err)
	}
	if n := phase3Count(t, db, `SELECT count(*) FROM offers`); n != 1 {
		t.Fatalf("terminal replay created an offer: %d", n)
	}
}
