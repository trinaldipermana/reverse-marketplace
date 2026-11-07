package commerce_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

var _ contracts.Commerce = (*commerce.Service)(nil)

func integratedService(t *testing.T) (*commerce.Service, *sql.DB) {
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
	s, err := commerce.New(db, commerce.Options{CheckoutOrigin: "http://checkout.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Inventory().SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	return s, db
}
func integratedLead(t *testing.T, s *commerce.Service, post string) domain.LeadRecord {
	t.Helper()
	lead, created, err := s.CreateLead(context.Background(), commerce.CreateLeadInput{SellerID: mockpos.DemoSellerID, Platform: "mock", PostID: post, BuyerID: "buyer_" + post, ThreadID: "thread_" + post, ItemID: "item_thinkpad", Intent: domain.Intent{IsBuyingRequest: true, Confidence: .95, ItemQuery: "coding laptop", Qty: 1, Location: domain.Location{City: "Jakarta"}}, MatchReason: "16GB coding laptop"})
	if err != nil || !created {
		t.Fatalf("create lead: %+v %v %v", lead, created, err)
	}
	return lead
}
func integratedRequest(h http.Handler, method, path, seller, key, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if seller != "" {
		request.Header.Set("X-Seller-ID", seller)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}
func TestCompletePurchaseUsesRealInventoryAndSafeHTTP(t *testing.T) {
	s, db := integratedService(t)
	ctx := context.Background()
	lead := integratedLead(t, s, "purchase")
	// The same concrete service satisfies P1's existing context loader.
	offer, err := s.RecordOffer(ctx, lead.ID, lead.ItemID, 5_600_000, 1, "offer")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := agentruntime.LoadLeadContext(ctx, s, lead.ID)
	if err != nil || loaded.Lead.Offer.ID != offer.ID {
		t.Fatalf("P1 context integration: %+v %v", loaded, err)
	}
	checkout, err := s.CreateCheckout(ctx, lead.ID, offer.ID, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.Inventory().Get(ctx, lead.SellerID, lead.ItemID)
	if err != nil || item.PhysicalStock != 2 || item.ReservedStock != 1 || item.AvailableStock != 1 {
		t.Fatalf("hold accounting: %+v %v", item, err)
	}
	if _, err = s.Inventory().Adjust(ctx, lead.SellerID, lead.ItemID, "blocked-pos", mockpos.AdjustStock{ExpectedVersion: item.Version, Delta: -2, Reason: "walk-in sale"}); err == nil {
		t.Fatal("POS consumed reserved stock")
	}
	parsed, err := url.Parse(checkout.URL)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(parsed.Path, "/checkout/")
	if token == "" {
		t.Fatal("missing token")
	}
	handler := s.Handler()
	detail := integratedRequest(handler, "GET", "/api/leads/"+lead.ID, lead.SellerID, "", "")
	if detail.Code != 200 {
		t.Fatalf("lead view: %d %s", detail.Code, detail.Body.String())
	}
	for _, secret := range []string{token, "floor_price", "FloorPrice", "checkout.test"} {
		if strings.Contains(detail.Body.String(), secret) {
			t.Fatalf("lead response exposed %q", secret)
		}
	}
	tokenView := integratedRequest(handler, "GET", "/api/checkouts/"+token, "", "", "")
	if tokenView.Code != 200 {
		t.Fatalf("checkout read: %d %s", tokenView.Code, tokenView.Body.String())
	}
	pay := integratedRequest(handler, "POST", "/api/checkouts/"+token+"/pay", "", "pay-one", `{}`)
	if pay.Code != 200 {
		t.Fatalf("pay: %d %s", pay.Code, pay.Body.String())
	}
	again := integratedRequest(handler, "POST", "/api/checkouts/"+token+"/pay", "", "pay-two", `{}`)
	if again.Code != 200 {
		t.Fatalf("repeat pay: %d %s", again.Code, again.Body.String())
	}
	item, err = s.Inventory().Get(ctx, lead.SellerID, lead.ItemID)
	if err != nil || item.PhysicalStock != 1 || item.ReservedStock != 0 || item.AvailableStock != 1 {
		t.Fatalf("payment double subtracted stock: %+v %v", item, err)
	}
	paid, err := s.GetLead(ctx, lead.ID)
	if err != nil || paid.State != domain.LeadPaid || paid.Checkout.Status != domain.CheckoutPaid {
		t.Fatalf("paid state: %+v %v", paid, err)
	}
	var payments, movements int
	if err = db.QueryRow(`SELECT count(*) FROM payments`).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM stock_movements WHERE kind='payment'`).Scan(&movements); err != nil {
		t.Fatal(err)
	}
	if payments != 1 || movements != 1 {
		t.Fatalf("duplicate commit payments=%d movements=%d", payments, movements)
	}
}
func TestLeadViewsAreSellerScopedAndPaginated(t *testing.T) {
	s, _ := integratedService(t)
	first := integratedLead(t, s, "first")
	integratedLead(t, s, "second")
	handler := s.Handler()
	response := integratedRequest(handler, "GET", "/api/leads?limit=1", mockpos.DemoSellerID, "", "")
	if response.Code != 200 {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	var page struct {
		Data struct {
			Items []json.RawMessage `json:"items"`
			Next  string            `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Data.Items) != 1 || page.Data.Next == "" {
		t.Fatalf("pagination: %s %v", response.Body.String(), err)
	}
	next := integratedRequest(handler, "GET", "/api/leads?limit=1&cursor="+url.QueryEscape(page.Data.Next), mockpos.DemoSellerID, "", "")
	if next.Code != 200 || next.Body.String() == response.Body.String() {
		t.Fatalf("second page: %d %s", next.Code, next.Body.String())
	}
	for _, test := range []struct {
		path, seller string
		status       int
	}{{"/api/leads/" + first.ID, "other", 404}, {"/api/leads/" + first.ID, "", 400}, {"/api/leads?state=unknown", mockpos.DemoSellerID, 400}, {"/api/leads?limit=0", mockpos.DemoSellerID, 400}, {"/api/leads?cursor=!", mockpos.DemoSellerID, 400}} {
		r := integratedRequest(handler, "GET", test.path, test.seller, "", "")
		if r.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, r.Code, r.Body.String())
		}
	}
}

func TestConfiguredOriginRejectsPathAndCredentialInjection(t *testing.T) {
	_, db := integratedService(t)
	for _, origin := range []string{"https://buyer.test/evil", "javascript:alert(1)", "https://name:secret@checkout.test", "https://checkout.test?redirect=evil", "https://checkout.test/#fragment"} {
		if _, err := commerce.New(db, commerce.Options{CheckoutOrigin: origin}); err == nil {
			t.Fatalf("accepted invalid origin %q", origin)
		}
	}
}

// This test exercises the actual P1 hook without sending a buyer message.
func TestPersistedLeadEventCanLoadCommerceInP1Worker(t *testing.T) {
	s, db := integratedService(t)
	lead := integratedLead(t, s, "worker")
	var eventID string
	if err := db.QueryRow(`SELECT event_id FROM runtime_notifications WHERE lead_id=?`, lead.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 2)
	worker, err := agentruntime.NewWorker(4, func(ctx context.Context, event agentruntime.Event) error {
		loaded, err := agentruntime.LoadLeadContext(ctx, s, event.LeadID)
		if err != nil {
			return err
		}
		received <- loaded.Lead.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- worker.Run(ctx) }()
	if err = worker.OnLeadDiscovered(ctx, eventID, lead.ID); err != nil {
		t.Fatal(err)
	}
	if err = worker.OnLeadDiscovered(ctx, eventID, lead.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-received:
		if id != lead.ID {
			t.Fatal("wrong lead")
		}
	case <-time.After(time.Second):
		t.Fatal("P1 did not load committed lead")
	}
	cancel()
	<-stopped
	if len(received) != 0 {
		t.Fatal("duplicate notification executed twice")
	}
}

func TestUpgradePreservesExistingPhase2Inventory(t *testing.T) {
	ctx := context.Background()
	db, err := mockpos.OpenSQLite(ctx, filepath.Join(t.TempDir(), "upgrade.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original, err := os.ReadFile("../../migrations/0001_p2_inventory.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(original)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations(name TEXT PRIMARY KEY); INSERT INTO schema_migrations(name) VALUES('0001_p2_inventory.sql')`); err != nil {
		t.Fatal(err)
	}
	old, err := mockpos.New(db, mockpos.Options{ReservedStock: mockpos.NoReservations})
	if err != nil {
		t.Fatal(err)
	}
	if err = old.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = old.Adjust(ctx, mockpos.DemoSellerID, "item_thinkpad", "before-upgrade", mockpos.AdjustStock{ExpectedVersion: 1, Delta: -1, Reason: "existing POS sale"}); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	current, err := commerce.New(db, commerce.Options{})
	if err != nil {
		t.Fatal(err)
	}
	item, err := current.Inventory().Get(ctx, mockpos.DemoSellerID, "item_thinkpad")
	if err != nil || item.PhysicalStock != 1 || item.AvailableStock != 1 || item.Version != 2 {
		t.Fatalf("migration changed stock/version: %+v %v", item, err)
	}
	var movements int
	if err = db.QueryRow(`SELECT count(*) FROM stock_movements WHERE item_id='item_thinkpad'`).Scan(&movements); err != nil {
		t.Fatal(err)
	}
	if movements != 2 {
		t.Fatalf("migration changed ledger count: %d", movements)
	}
	replay, err := current.Inventory().Adjust(ctx, mockpos.DemoSellerID, "item_thinkpad", "before-upgrade", mockpos.AdjustStock{ExpectedVersion: 1, Delta: -1, Reason: "existing POS sale"})
	if err != nil || replay.PhysicalStock != 1 {
		t.Fatalf("upgrade lost idempotency: %+v %v", replay, err)
	}
}
