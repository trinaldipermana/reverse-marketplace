package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"strings"
	"testing"
)

type generatorFunc func(context.Context, llm.JSONRequest, any) error

func (f generatorFunc) GenerateJSON(c context.Context, r llm.JSONRequest, o any) error {
	return f(c, r, o)
}
func modelFixture() []domain.Item {
	return []domain.Item{{ID: "laptop", Name: "ThinkPad 16GB", Attributes: map[string]string{"ram_gb": "16"}, ListPriceIDR: 5800000}}
}
func TestQueryPlanNormalizesAndPreservesSources(t *testing.T) {
	m, _ := NewModelAdapter(generatorFunc(func(_ context.Context, r llm.JSONRequest, out any) error {
		if !json.Valid(r.Schema) || !strings.Contains(r.Instructions, "AND") || strings.Contains(r.Input, "FloorPrice") {
			t.Fatal("unsafe request")
		}
		return json.Unmarshal([]byte(`{"queries":[{"query":" Laptop  coding ","item_ids":["laptop"]},{"query":"laptop coding","item_ids":["laptop"]}]}`), out)
	}))
	q, err := m.PlanQueries(context.Background(), modelFixture(), 2)
	if err != nil || len(q) != 1 || len(q[0].ItemIDs) != 1 {
		t.Fatalf("%+v %v", q, err)
	}
}
func TestUnknownQueryItemAndProviderFailure(t *testing.T) {
	sentinel := errors.New("provider failure")
	for _, body := range []string{`{"queries":[{"query":"laptop","item_ids":["invented"]}]}`, `{"queries":[]}`} {
		m, _ := NewModelAdapter(generatorFunc(func(_ context.Context, _ llm.JSONRequest, out any) error { return json.Unmarshal([]byte(body), out) }))
		if _, err := m.PlanQueries(context.Background(), modelFixture(), 2); err == nil {
			t.Fatal("invalid plan accepted")
		}
	}
	m, _ := NewModelAdapter(generatorFunc(func(context.Context, llm.JSONRequest, any) error { return sentinel }))
	if _, err := m.PlanQueries(context.Background(), modelFixture(), 2); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

const validMatch = `{"is_buying_request":true,"confidence":0.95,"item_query":"coding laptop","quantity":2,"requirements":[{"name":"ram_gb_min","value":"16"}],"city":"Jakarta","budget_amount":6000000,"budget_currency":"IDR","urgency_text":"this week","needed_by":null,"matches":[{"item_id":"laptop","score":0.9,"reason":"16GB RAM"}]}`

func TestMatchPreservesQuantityBudgetRequestedCityAndTrustedCoordinates(t *testing.T) {
	m, _ := NewModelAdapter(generatorFunc(func(_ context.Context, r llm.JSONRequest, out any) error {
		if !json.Valid(r.Schema) || !strings.Contains(r.Instructions, "6000000 IDR") {
			t.Fatal("invalid schema/prompt")
		}
		return json.Unmarshal([]byte(validMatch), out)
	}))
	latitude, longitude := -6.9, 107.6
	post := domain.Post{Text: "Butuh laptop 6 juta untuk Jakarta", Location: domain.Location{City: "Bandung", Latitude: &latitude, Longitude: &longitude}}
	got, err := m.MatchPost(context.Background(), post, modelFixture())
	if err != nil || got.Intent.Qty != 2 || *got.Intent.BudgetIDR != 6000000 || got.BudgetCurrency != "IDR" || got.Intent.Location.City != "Jakarta" || got.Intent.Location.Latitude == nil || *got.Intent.Location.Latitude != latitude || got.Intent.Location.Longitude == nil || *got.Intent.Location.Longitude != longitude || post.Location.City != "Bandung" {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestMatchRejectsInventedItemsAndBadSemantics(t *testing.T) {
	for _, body := range []string{strings.Replace(validMatch, `"item_id":"laptop"`, `"item_id":"invented"`, 1), strings.Replace(validMatch, `"confidence":0.95`, `"confidence":2`, 1), strings.Replace(validMatch, `"is_buying_request":true`, `"is_buying_request":false`, 1), strings.Replace(validMatch, `"budget_currency":"IDR"`, `"budget_currency":null`, 1)} {
		m, _ := NewModelAdapter(generatorFunc(func(_ context.Context, _ llm.JSONRequest, out any) error { return json.Unmarshal([]byte(body), out) }))
		if _, err := m.MatchPost(context.Background(), domain.Post{}, modelFixture()); err == nil {
			t.Fatal("invalid match accepted")
		}
	}
}
func TestCancelledModelCallsDoNotInvokeGenerator(t *testing.T) {
	m, _ := NewModelAdapter(generatorFunc(func(context.Context, llm.JSONRequest, any) error { t.Fatal("called cancelled generator"); return nil }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.PlanQueries(ctx, modelFixture(), 2); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := m.MatchPost(ctx, domain.Post{}, modelFixture()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
