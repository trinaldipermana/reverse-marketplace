package discovery_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/discovery"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

type scriptedGenerator struct {
	mu                     sync.Mutex
	queryCalls, matchCalls int
	observedInputs         []string
}

func (g *scriptedGenerator) GenerateJSON(ctx context.Context, request llm.JSONRequest, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observedInputs = append(g.observedInputs, request.Input)
	if strings.Contains(request.Input, "5400000") {
		return errors.New("private floor leaked into model input")
	}
	if request.SchemaName == "inventory_queries" {
		g.queryCalls++
		return json.Unmarshal([]byte(`{"queries":[{"query":"laptop","item_ids":["item_thinkpad","item_dell","item_hp"]},{"query":"ThinkPad","item_ids":["item_thinkpad"]}]}`), out)
	}
	g.matchCalls++
	var input struct {
		Post struct {
			Text string `json:"text"`
		} `json:"post"`
	}
	if err := json.Unmarshal([]byte(request.Input), &input); err != nil {
		return err
	}
	buying := !strings.Contains(input.Post.Text, "Dijual")
	ram := "16"
	if strings.Contains(input.Post.Text, "32GB") {
		ram = "32"
	}
	matches := []map[string]any{}
	if buying {
		matches = append(matches, map[string]any{"item_id": "item_thinkpad", "score": .94, "reason": "Laptop with known RAM specification"})
	}
	raw, err := json.Marshal(map[string]any{"is_buying_request": buying, "confidence": .96, "item_query": "used laptop", "quantity": 1, "requirements": []map[string]string{{"name": "ram_gb_min", "value": ram}}, "city": "Jakarta", "budget_amount": 6000000, "budget_currency": "IDR", "urgency_text": "this week", "needed_by": nil, "matches": matches})
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

type notificationStub struct {
	mu     sync.Mutex
	events []string
	fail   bool
}

func (n *notificationStub) OnLeadDiscovered(ctx context.Context, event, lead string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, event)
	if n.fail {
		return errors.New("temporary runtime failure")
	}
	return nil
}
func base(t *testing.T) (*sql.DB, *commerce.Service, *mock.PostStore) {
	t.Helper()
	ctx := context.Background()
	db, err := mockpos.OpenSQLite(ctx, filepath.Join(t.TempDir(), "discovery.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	service, err := commerce.New(db, commerce.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Inventory().SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	posts := []domain.Post{}
	for i, body := range []string{"Need a laptop ThinkPad 16GB under 6 juta in Jakarta", "Dijual laptop bekas", "Need a laptop with 32GB under 6 juta in Jakarta"} {
		id := []string{"buying", "selling", "mismatch"}[i]
		posts = append(posts, domain.Post{PostRef: domain.PostRef{Platform: "mock", ExternalPostID: id}, BuyerID: "buyer_" + id, ThreadID: "thread_" + id, Text: body, CreatedAt: now, Location: domain.Location{City: "Jakarta"}})
	}
	store, err := mock.NewPostStore(posts...)
	if err != nil {
		t.Fatal(err)
	}
	return db, service, store
}
func modelService(t *testing.T, db *sql.DB, commerce *commerce.Service, posts contracts.MarketplaceDiscovery, g discovery.JSONGenerator, n discovery.LeadNotifier, options discovery.Options) *discovery.Service {
	t.Helper()
	adapter, err := discovery.NewModelAdapter(g)
	if err != nil {
		t.Fatal(err)
	}
	s, err := discovery.New(db, commerce, posts, adapter, adapter, n, options)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func dbCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func TestSynchronousDiscoveryWithActualMarketplaceCommerceAndP1(t *testing.T) {
	db, commerce, posts := base(t)
	generator := &scriptedGenerator{}
	accepted := make(chan string, 4)
	worker, err := agentruntime.NewWorker(8, func(ctx context.Context, event agentruntime.Event) error {
		loaded, err := agentruntime.LoadLeadContext(ctx, commerce, event.LeadID)
		if err != nil {
			return err
		}
		accepted <- loaded.Lead.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(workerCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	discovery := modelService(t, db, commerce, posts, generator, worker, discovery.Options{})
	request := httptest.NewRequest("POST", "/api/matches/search", strings.NewReader(`{"sellerId":"seller_demo"}`))
	response := httptest.NewRecorder()
	discovery.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("discovery HTTP: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Matches []struct {
			LeadID       string `json:"leadId"`
			Notification string `json:"notificationStatus"`
		} `json:"matches"`
		Requests int `json:"requestsScanned"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Matches) != 1 || result.Requests != 3 {
		t.Fatalf("result: %s %v", response.Body.String(), err)
	}
	if result.Matches[0].Notification != "accepted" {
		t.Fatal("notification was not accepted")
	}
	select {
	case lead := <-accepted:
		if lead != result.Matches[0].LeadID {
			t.Fatal("P1 loaded wrong lead")
		}
	case <-time.After(time.Second):
		t.Fatal("P1 did not receive committed lead")
	}
	if generator.matchCalls != 3 {
		t.Fatalf("overlap rematched posts %d times", generator.matchCalls)
	}
	if n := dbCount(t, db, `SELECT count(*) FROM discovery_query_posts`); n != 4 {
		t.Fatalf("query/post map lost overlap: %d", n)
	}
	if n := dbCount(t, db, `SELECT count(*) FROM discovery_posts`); n != 3 {
		t.Fatalf("post snapshot duplication: %d", n)
	}
	if n := dbCount(t, db, `SELECT count(*) FROM discovery_posts WHERE matching_status='completed'`); n != 3 {
		t.Fatalf("lost extraction evidence for ignored posts: %d", n)
	}
	if n := dbCount(t, db, `SELECT count(*) FROM leads`); n != 1 {
		t.Fatalf("invalid lead creation count %d", n)
	}
	again, err := discovery.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(again.Matches) != 1 {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	if n := dbCount(t, db, `SELECT count(*) FROM runtime_notifications`); n != 1 {
		t.Fatalf("duplicate notification intents: %d", n)
	}
	if n := dbCount(t, db, `SELECT attempts FROM runtime_notifications`); n != 1 {
		t.Fatalf("accepted event was retried: %d", n)
	}
	if n := dbCount(t, db, `SELECT count(*) FROM discovery_runs WHERE status='running'`); n != 0 {
		t.Fatal("request left an active run")
	}
}
func TestPendingNotificationRetryUsesSameEvent(t *testing.T) {
	db, commerce, posts := base(t)
	generator := &scriptedGenerator{}
	notify := &notificationStub{fail: true}
	s := modelService(t, db, commerce, posts, generator, notify, discovery.Options{})
	first, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(first.Matches) != 1 || first.Matches[0].NotificationStatus == "accepted" {
		t.Fatalf("failed dispatch not visible: %+v %v", first, err)
	}
	notify.fail = false
	second, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(second.Matches) != 1 || second.Matches[0].NotificationStatus != "accepted" {
		t.Fatalf("retry: %+v %v", second, err)
	}
	if len(notify.events) != 2 || notify.events[0] != notify.events[1] {
		t.Fatalf("event ID changed: %+v", notify.events)
	}
	if n := dbCount(t, db, `SELECT count(*) FROM leads`); n != 1 {
		t.Fatal("retry duplicated lead")
	}
}
func TestEmptyInventorySkipsModelAndSearch(t *testing.T) {
	db, commerce, posts := base(t)
	if _, err := db.Exec(`UPDATE inventory_items SET active=0`); err != nil {
		t.Fatal(err)
	}
	generator := &scriptedGenerator{}
	s := modelService(t, db, commerce, posts, generator, &notificationStub{}, discovery.Options{})
	result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(result.Matches) != 0 || generator.queryCalls != 0 || generator.matchCalls != 0 {
		t.Fatalf("empty inventory: %+v %v", result, err)
	}
}
func TestDiscoveryRequestContractAndFailureMasking(t *testing.T) {
	db, commerce, posts := base(t)
	s := modelService(t, db, commerce, posts, &scriptedGenerator{}, &notificationStub{}, discovery.Options{})
	for _, body := range []string{`{}`, `{"seller_id":"seller_demo"}`, `{"sellerId":"seller_demo","extra":true}`, `{"sellerId":null}`, `{} {}`, `{"sellerId":" seller_demo"}`} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest("POST", "/api/matches/search", strings.NewReader(body)))
		if response.Code != 400 {
			t.Fatalf("accepted %s: %d %s", body, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest("POST", "/api/matches/search", strings.NewReader(`{"sellerId":"missing"}`)))
	if response.Code != 404 {
		t.Fatalf("missing seller: %d %s", response.Code, response.Body.String())
	}
	db.Close()
	response = httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest("POST", "/api/matches/search", strings.NewReader(`{"sellerId":"seller_demo"}`)))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "database is closed") {
		t.Fatalf("unsafe failure: %d %s", response.Code, response.Body.String())
	}
}
