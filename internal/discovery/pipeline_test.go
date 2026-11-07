package discovery

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

type plannerFunc func(context.Context, []domain.Item, int) ([]SearchQuery, error)

func (f plannerFunc) PlanQueries(ctx context.Context, items []domain.Item, max int) ([]SearchQuery, error) {
	return f(ctx, items, max)
}

type matcherFunc func(context.Context, domain.Post, []domain.Item) (PostMatch, error)

func (f matcherFunc) MatchPost(ctx context.Context, post domain.Post, items []domain.Item) (PostMatch, error) {
	return f(ctx, post, items)
}

type notifyCall struct{ Event, Lead string }
type notificationFake struct {
	mu    sync.Mutex
	calls []notifyCall
	fail  bool
}

func (n *notificationFake) OnLeadDiscovered(_ context.Context, event, lead string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, notifyCall{event, lead})
	if n.fail {
		return errors.New("unavailable runtime with private detail")
	}
	return nil
}
func (n *notificationFake) snapshot() []notifyCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notifyCall(nil), n.calls...)
}

type searchAdapter struct {
	contracts.MarketplaceDiscovery
	search func(context.Context, contracts.SearchPostRequest) (contracts.SearchPostResult, error)
}

func (s searchAdapter) SearchPost(ctx context.Context, req contracts.SearchPostRequest) (contracts.SearchPostResult, error) {
	return s.search(ctx, req)
}

func pipelinePost(id, text, city string) domain.Post {
	return domain.Post{PostRef: domain.PostRef{Platform: "mock", ExternalPostID: id}, BuyerID: "buyer-" + id, ThreadID: "thread-" + id, Text: text, CreatedAt: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), Location: domain.Location{City: city}}
}
func pipelinePlan(_ context.Context, items []domain.Item, max int) ([]SearchQuery, error) {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return []SearchQuery{{Query: "laptop", ItemIDs: ids}, {Query: "coding laptop", ItemIDs: ids}}, nil
}
func pipelineMatch(_ context.Context, post domain.Post, _ []domain.Item) (PostMatch, error) {
	if strings.Contains(post.Text, "Dijual") {
		return PostMatch{Intent: domain.Intent{IsBuyingRequest: false, Confidence: .99}}, nil
	}
	budget := int64(6_000_000)
	return PostMatch{Intent: domain.Intent{IsBuyingRequest: true, Confidence: .98, ItemQuery: "coding laptop", Qty: 1, Location: domain.Location{City: "Jakarta"}, BudgetIDR: &budget, Requirements: []domain.Requirement{{Name: "ram_gb_min", Value: "16"}}}, BudgetCurrency: "IDR", Matches: []ItemMatch{{ItemID: "item_dell", Score: .9, Reason: "16GB RAM for coding"}, {ItemID: "item_hp", Score: .99, Reason: "Claimed enough memory"}, {ItemID: "item_thinkpad", Score: .9, Reason: "16GB RAM for coding"}}}, nil
}
func setupPipeline(t *testing.T, posts []domain.Post, planner QueryPlanner, matcher PostMatcher, notifier LeadNotifier, opts Options) (*Service, *commerce.Service, *sql.DB, *mock.PostStore) {
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
	core, err := commerce.New(db, commerce.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = core.Inventory().SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := mock.NewPostStore(posts...)
	if err != nil {
		t.Fatal(err)
	}
	if planner == nil {
		planner = plannerFunc(pipelinePlan)
	}
	if matcher == nil {
		matcher = matcherFunc(pipelineMatch)
	}
	if notifier == nil {
		notifier = &notificationFake{}
	}
	s, err := New(db, core, store, planner, matcher, notifier, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s, core, db, store
}
func pipelineCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func pipelineCode(t *testing.T, err error, wanted contracts.Code) {
	t.Helper()
	var failure *contracts.Error
	if !errors.As(err, &failure) || failure.Code != wanted {
		t.Fatalf("got %v, want %s", err, wanted)
	}
}

func TestDiscoverDeduplicatesQueriesPostsLeadsAndEvents(t *testing.T) {
	notify := &notificationFake{}
	counts := map[string]int{}
	matcher := matcherFunc(func(ctx context.Context, post domain.Post, items []domain.Item) (PostMatch, error) {
		counts[post.ExternalPostID]++
		return pipelineMatch(ctx, post, items)
	})
	posts := []domain.Post{pipelinePost("buy", "Need coding laptop 16GB under 6 juta in Jakarta", ""), pipelinePost("sell", "Dijual coding laptop 16GB", "Jakarta")}
	s, core, db, _ := setupPipeline(t, posts, nil, matcher, notify, Options{})
	result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.RequestsScanned != 2 || result.ListingsScanned != 3 {
		t.Fatalf("result: %+v", result)
	}
	match := result.Matches[0]
	if match.Product.ID != "item_thinkpad" || match.MatchScore != 90 || match.Product.Price != "Rp5.800.000" || match.NotificationStatus != "accepted" {
		t.Fatalf("selected: %+v", match)
	}
	if counts["buy"] != 1 || counts["sell"] != 1 {
		t.Fatalf("overlap evaluated multiple times: %v", counts)
	}
	if n := pipelineCount(t, db, `SELECT count(*) FROM discovery_query_posts WHERE run_id=?`, result.RunID); n != 4 {
		t.Fatalf("lost query provenance: %d", n)
	}
	if n := pipelineCount(t, db, `SELECT count(*) FROM discovery_posts WHERE run_id=? AND matching_status='completed'`, result.RunID); n != 2 {
		t.Fatalf("missing negative/extraction evidence: %d", n)
	}
	if n := pipelineCount(t, db, `SELECT count(*) FROM discovery_matches WHERE run_id=? AND status='rejected' AND decision_reason='ATTRIBUTES'`, result.RunID); n != 1 {
		t.Fatal("high model score overrode RAM check")
	}
	if len(notify.snapshot()) != 1 {
		t.Fatal("multiple initial runtime notifications")
	}
	firstLead := match.LeadID
	repeated, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(repeated.Matches) != 1 || repeated.Matches[0].LeadID != firstLead || len(notify.snapshot()) != 1 {
		t.Fatalf("repeat caused new outreach: %+v %v", repeated, err)
	}
	if n := pipelineCount(t, db, `SELECT count(*) FROM leads`); n != 1 {
		t.Fatal("repeated discovery duplicated lead")
	}
	loaded, err := core.GetLead(context.Background(), firstLead)
	if err != nil || loaded.ItemID != "item_thinkpad" {
		t.Fatalf("stored lead: %+v %v", loaded, err)
	}
}

func TestDiscoveryNotificationFailureRetriesSamePersistedEvent(t *testing.T) {
	notify := &notificationFake{fail: true}
	s, _, db, _ := setupPipeline(t, []domain.Post{pipelinePost("buy", "Need coding laptop", "Jakarta")}, nil, nil, notify, Options{})
	first, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(first.Matches) != 1 || first.Matches[0].NotificationStatus != "failed" || len(first.Warnings) == 0 {
		t.Fatalf("notify failure lost lead: %+v %v", first, err)
	}
	notify.fail = false
	second, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || second.Matches[0].NotificationStatus != "accepted" {
		t.Fatalf("notify retry: %+v %v", second, err)
	}
	calls := notify.snapshot()
	if len(calls) != 2 || calls[0] != calls[1] {
		t.Fatalf("notification ID changed: %+v", calls)
	}
	if n := pipelineCount(t, db, `SELECT count(*) FROM leads`); n != 1 {
		t.Fatal("notification retry recreated lead")
	}
}

func TestDiscoveryEmptyInventorySkipsModelAndSearch(t *testing.T) {
	planner := plannerFunc(func(context.Context, []domain.Item, int) ([]SearchQuery, error) {
		t.Fatal("planner called without inventory")
		return nil, nil
	})
	s, _, db, _ := setupPipeline(t, nil, planner, nil, nil, Options{})
	if _, err := db.Exec(`UPDATE inventory_items SET physical_stock=0`); err != nil {
		t.Fatal(err)
	}
	result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(result.Matches) != 0 || result.RequestsScanned != 0 || result.ListingsScanned != 0 {
		t.Fatalf("empty inventory: %+v %v", result, err)
	}
}

func TestDiscoveryPlanningUsesCurrentInventory(t *testing.T) {
	names := []string{}
	planner := plannerFunc(func(_ context.Context, items []domain.Item, _ int) ([]SearchQuery, error) {
		for _, i := range items {
			if i.ID == "item_thinkpad" {
				names = append(names, i.Name)
			}
		}
		return []SearchQuery{{Query: items[0].Name, ItemIDs: []string{items[0].ID}}}, nil
	})
	s, core, _, _ := setupPipeline(t, nil, planner, nil, nil, Options{})
	if _, err := s.Discover(context.Background(), mockpos.DemoSellerID); err != nil {
		t.Fatal(err)
	}
	name := "Changed live inventory title"
	if _, err := core.Inventory().Patch(context.Background(), mockpos.DemoSellerID, "item_thinkpad", "rename", mockpos.PatchItem{ExpectedVersion: 1, Name: &name}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Discover(context.Background(), mockpos.DemoSellerID); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] == names[1] || names[1] != name {
		t.Fatalf("static/stale inventory used: %v", names)
	}
}

func TestDiscoveryPartialAndTotalSearchFailure(t *testing.T) {
	s, _, db, store := setupPipeline(t, []domain.Post{pipelinePost("buy", "Need coding laptop", "Jakarta")}, nil, nil, nil, Options{})
	s.marketplace = searchAdapter{MarketplaceDiscovery: store, search: func(ctx context.Context, req contracts.SearchPostRequest) (contracts.SearchPostResult, error) {
		if req.Query == "laptop" {
			return contracts.SearchPostResult{}, errors.New("private search failure")
		}
		return store.SearchPost(ctx, req)
	}}
	result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(result.Matches) != 1 || len(result.Warnings) == 0 {
		t.Fatalf("partial: %+v %v", result, err)
	}
	var state string
	if err = db.QueryRow(`SELECT status FROM discovery_runs WHERE id=?`, result.RunID).Scan(&state); err != nil || state != "partial" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	s.marketplace = searchAdapter{MarketplaceDiscovery: store, search: func(context.Context, contracts.SearchPostRequest) (contracts.SearchPostResult, error) {
		return contracts.SearchPostResult{}, errors.New("private search failure")
	}}
	_, err = s.Discover(context.Background(), mockpos.DemoSellerID)
	pipelineCode(t, err, contracts.DependencyUnavailable)
	if strings.Contains(err.Error(), "private") {
		t.Fatal("raw search failure exposed")
	}
}

func TestDiscoveryMatchingFailureIsNotEmptySuccess(t *testing.T) {
	matcher := matcherFunc(func(context.Context, domain.Post, []domain.Item) (PostMatch, error) {
		return PostMatch{}, errors.New("private model response")
	})
	s, _, db, _ := setupPipeline(t, []domain.Post{pipelinePost("buy", "Need coding laptop", "Jakarta")}, nil, matcher, nil, Options{})
	result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	pipelineCode(t, err, contracts.DependencyUnavailable)
	if n := pipelineCount(t, db, `SELECT count(*) FROM discovery_posts WHERE run_id=? AND matching_status='failed'`, result.RunID); n != 1 {
		t.Fatal("model failure not persisted")
	}
	if n := pipelineCount(t, db, `SELECT count(*) FROM leads`); n != 0 {
		t.Fatal("model failure created lead")
	}
}

func TestDiscoveryBudgetsPreserveProvenanceWithoutExtraMatching(t *testing.T) {
	matched := 0
	matcher := matcherFunc(func(ctx context.Context, p domain.Post, items []domain.Item) (PostMatch, error) {
		matched++
		return pipelineMatch(ctx, p, items)
	})
	s, _, db, _ := setupPipeline(t, []domain.Post{pipelinePost("a", "Need coding laptop", "Jakarta"), pipelinePost("b", "Need coding laptop", "Jakarta"), pipelinePost("c", "Need coding laptop", "Jakarta")}, nil, matcher, nil, Options{MaxPosts: 1})
	result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || matched != 1 || result.RequestsScanned != 1 || !result.Truncated {
		t.Fatalf("budget: %+v matched=%d %v", result, matched, err)
	}
	if n := pipelineCount(t, db, `SELECT count(*) FROM discovery_query_posts WHERE run_id=?`, result.RunID); n != 6 {
		t.Fatalf("budget discarded query provenance: %d", n)
	}
}

func TestDiscoveryCancellationFinalizesRunAndConcurrentStartRejects(t *testing.T) {
	started := make(chan struct{})
	planner := plannerFunc(func(ctx context.Context, _ []domain.Item, _ int) ([]SearchQuery, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	s, _, db, _ := setupPipeline(t, nil, planner, nil, nil, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { _, err := s.Discover(ctx, mockpos.DemoSellerID); finished <- err }()
	<-started
	_, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	pipelineCode(t, err, contracts.InvalidState)
	cancel()
	if err = <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if n := pipelineCount(t, db, `SELECT count(*) FROM discovery_runs WHERE status='running'`); n != 0 {
		t.Fatal("cancel left seller locked")
	}
}

func TestRediscoveryKeepsExistingItemWithoutMisleadingScore(t *testing.T) {
	s, core, _, _ := setupPipeline(t, []domain.Post{pipelinePost("buy", "Need coding laptop", "Jakarta")}, nil, nil, nil, Options{})
	existing, _, err := core.CreateLead(context.Background(), commerce.CreateLeadInput{SellerID: mockpos.DemoSellerID, Platform: "mock", PostID: "buy", BuyerID: "buyer-buy", ThreadID: "thread-buy", ItemID: "item_dell", Intent: domain.Intent{IsBuyingRequest: true, Confidence: .9, ItemQuery: "Laptop", Qty: 1}, MatchReason: "Previously selected Dell"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(result.Matches) != 1 {
		t.Fatalf("rediscovery: %+v %v", result, err)
	}
	match := result.Matches[0]
	if match.LeadID != existing.ID || match.Product.ID != "item_dell" || match.MatchScore != 0 || !strings.Contains(match.MatchReasons[0], "Existing lead") {
		t.Fatalf("fresh other-item score leaked: %+v", match)
	}
}

func TestTerminalPendingLeadIsNotDispatchedByRediscovery(t *testing.T) {
	notify := &notificationFake{}
	s, core, db, _ := setupPipeline(t, []domain.Post{pipelinePost("buy", "Need coding laptop", "Jakarta")}, nil, nil, notify, Options{})
	lead, _, err := core.CreateLead(context.Background(), commerce.CreateLeadInput{SellerID: mockpos.DemoSellerID, Platform: "mock", PostID: "buy", BuyerID: "buyer-buy", ThreadID: "thread-buy", ItemID: "item_thinkpad", Intent: domain.Intent{IsBuyingRequest: true, Confidence: .9, ItemQuery: "Laptop", Qty: 1}, MatchReason: "Previously selected laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = core.TransitionLead(context.Background(), lead.ID, domain.LeadLost, "buyer declined"); err != nil {
		t.Fatal(err)
	}
	result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(result.Matches) != 1 || result.Matches[0].LeadState != domain.LeadLost || result.Matches[0].NotificationStatus != "paused" {
		t.Fatalf("terminal rediscovery: %+v %v", result, err)
	}
	if len(notify.snapshot()) != 0 {
		t.Fatal("terminal lead reached runtime")
	}
	if n := pipelineCount(t, db, `SELECT attempts FROM runtime_notifications WHERE lead_id=?`, lead.ID); n != 0 {
		t.Fatal("terminal lead counted notification attempt")
	}
}

func TestDiscoveryIntentAndLocationGatesNeverCreateLead(t *testing.T) {
	tests := []struct {
		name   string
		change func(*PostMatch)
	}{
		{"low confidence", func(m *PostMatch) { m.Intent.Confidence = .5 }},
		{"non buying", func(m *PostMatch) { m.Intent.IsBuyingRequest = false }},
		{"multiple units", func(m *PostMatch) { m.Intent.Qty = 2 }},
		{"non IDR", func(m *PostMatch) { m.BudgetCurrency = "USD" }},
		{"unknown location", func(m *PostMatch) { m.Intent.Location = domain.Location{} }},
		{"low match score", func(m *PostMatch) {
			for i := range m.Matches {
				m.Matches[i].Score = .5
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := matcherFunc(func(ctx context.Context, p domain.Post, items []domain.Item) (PostMatch, error) {
				m, err := pipelineMatch(ctx, p, items)
				tt.change(&m)
				return m, err
			})
			s, _, db, _ := setupPipeline(t, []domain.Post{pipelinePost("buy", "Need coding laptop", "")}, nil, matcher, nil, Options{})
			result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
			if err != nil || len(result.Matches) != 0 || pipelineCount(t, db, `SELECT count(*) FROM leads`) != 0 {
				t.Fatalf("gate allowed lead: %+v %v", result, err)
			}
		})
	}
}

func TestDiscoveryRechecksStockAfterMatching(t *testing.T) {
	var core *commerce.Service
	matcher := matcherFunc(func(ctx context.Context, p domain.Post, items []domain.Item) (PostMatch, error) {
		if _, err := core.Inventory().Adjust(ctx, mockpos.DemoSellerID, "item_thinkpad", "external-pos", mockpos.AdjustStock{ExpectedVersion: 1, Delta: -2, Reason: "walk-in sale during matching"}); err != nil {
			return PostMatch{}, err
		}
		matched, err := pipelineMatch(ctx, p, items)
		matched.Matches = []ItemMatch{{ItemID: "item_thinkpad", Score: .99, Reason: "Snapshot RAM match"}}
		return matched, err
	})
	s, c, db, _ := setupPipeline(t, []domain.Post{pipelinePost("buy", "Need coding laptop", "Jakarta")}, nil, matcher, nil, Options{})
	core = c
	result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || len(result.Matches) != 0 {
		t.Fatalf("stale stock created lead: %+v %v", result, err)
	}
	if n := pipelineCount(t, db, `SELECT count(*) FROM discovery_matches WHERE decision_reason='NO_STOCK'`); n != 1 {
		t.Fatal("current stock not checked")
	}
}

func TestDiscoveryStopsRepeatedCursor(t *testing.T) {
	s, _, _, store := setupPipeline(t, nil, nil, nil, nil, Options{MaxPagesPerQuery: 5})
	s.planner = plannerFunc(func(context.Context, []domain.Item, int) ([]SearchQuery, error) {
		return []SearchQuery{{Query: "laptop", ItemIDs: []string{"item_thinkpad"}}}, nil
	})
	calls := 0
	s.marketplace = searchAdapter{MarketplaceDiscovery: store, search: func(context.Context, contracts.SearchPostRequest) (contracts.SearchPostResult, error) {
		calls++
		return contracts.SearchPostResult{Posts: []domain.Post{pipelinePost("buy", "Need coding laptop", "Jakarta")}, NextCursor: "same-cursor"}, nil
	}}
	result, err := s.Discover(context.Background(), mockpos.DemoSellerID)
	if err != nil || calls != 2 || !result.Truncated || result.RequestsScanned != 1 {
		t.Fatalf("cursor loop: %+v calls=%d err=%v", result, calls, err)
	}
}
