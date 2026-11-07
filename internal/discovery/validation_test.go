package discovery

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func valuePointer[T any](v T) *T { return &v }
func TestCandidateHardConstraintsOverrideRanking(t *testing.T) {
	s, _, _, _ := setupPipeline(t, nil, nil, nil, nil, Options{})
	post := pipelinePost("buy", "Need coding laptop", "Jakarta")
	baseline, _ := pipelineMatch(context.Background(), post, nil)
	tests := []struct {
		name   string
		change func(*PostMatch, *ItemMatch)
		reason string
	}{
		{"fits below list via legal discount", func(m *PostMatch, _ *ItemMatch) { m.Intent.BudgetIDR = valuePointer(int64(5_500_000)) }, ""},
		{"too low even at floor", func(m *PostMatch, _ *ItemMatch) { m.Intent.BudgetIDR = valuePointer(int64(5_300_000)) }, "BUDGET"},
		{"unsupported currency", func(m *PostMatch, _ *ItemMatch) { m.BudgetCurrency = "USD" }, "CURRENCY"},
		{"wrong quantity", func(m *PostMatch, _ *ItemMatch) { m.Intent.Qty = 2 }, "QUANTITY"},
		{"invented item", func(_ *PostMatch, c *ItemMatch) { c.ItemID = "invented" }, "UNKNOWN_ITEM"},
		{"low score", func(_ *PostMatch, c *ItemMatch) { c.Score = .5 }, "MATCH_THRESHOLD"},
		{"nonfinite score", func(_ *PostMatch, c *ItemMatch) { c.Score = math.NaN() }, "INVALID_MATCH"},
		{"high score insufficient RAM", func(_ *PostMatch, c *ItemMatch) { c.ItemID = "item_hp"; c.Score = 1 }, "ATTRIBUTES"},
		{"unknown hard attribute", func(m *PostMatch, _ *ItemMatch) {
			m.Intent.Requirements = []domain.Requirement{{Name: "warranty_months_min", Value: "12"}}
		}, "ATTRIBUTES"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched := baseline
			candidate := ItemMatch{ItemID: "item_thinkpad", Score: .95, Reason: "Trusted RAM match"}
			tt.change(&matched, &candidate)
			_, reason, err := s.validateCandidate(context.Background(), mockpos.DemoSellerID, post, matched, candidate, map[string]bool{"item_thinkpad": true, "item_hp": true})
			if err != nil || reason != tt.reason {
				t.Fatalf("reason=%s want=%s err=%v", reason, tt.reason, err)
			}
		})
	}
}
func TestRequirementsUseKnownFactsOnly(t *testing.T) {
	item := domain.Item{Attributes: map[string]string{"RAM_GB": "16", "condition": "Used", "storage_gb": "512"}}
	for _, test := range []struct {
		name, value string
		want        bool
	}{{"ram_gb_min", "16", true}, {"ram_gb_min", "32", false}, {"storage_gb_max", "1024", true}, {"condition", "used", true}, {"condition", "new", false}, {"missing", "value", false}, {"ram_gb_min", "NaN", false}, {"ram_gb_max", "+Inf", false}} {
		if got := requirementsMatch([]domain.Requirement{{Name: test.name, Value: test.value}}, item); got != test.want {
			t.Fatalf("%s=%s: %v", test.name, test.value, got)
		}
	}
}
func TestLocationUsesTrustedCoordinatesAndExplicitCityFallback(t *testing.T) {
	jakarta := domain.Location{City: "Jakarta"}
	coordinateJakarta := domain.Location{City: "Jakarta", Latitude: valuePointer(-6.2), Longitude: valuePointer(106.8)}
	nearby := domain.Location{City: "Jakarta", Latitude: valuePointer(-6.21), Longitude: valuePointer(106.81)}
	distance, reason := locationMatch(coordinateJakarta, jakarta, nearby, 50)
	if reason != "" || distance <= 0 || distance > 5 {
		t.Fatalf("nearby distance %f %s", distance, reason)
	}
	far := domain.Location{City: "Bandung", Latitude: valuePointer(-6.9), Longitude: valuePointer(107.6)}
	if _, reason = locationMatch(coordinateJakarta, jakarta, far, 50); reason != "DISTANCE" {
		t.Fatalf("far: %s", reason)
	}
	if _, reason = locationMatch(jakarta, domain.Location{City: "Bandung"}, jakarta, 50); reason != "LOCATION_CONFLICT" {
		t.Fatalf("contradictory city: %s", reason)
	}
	distance, reason = locationMatch(domain.Location{}, jakarta, jakarta, 50)
	if reason != "" || !math.IsInf(distance, 1) {
		t.Fatalf("city fallback invented distance: %f %s", distance, reason)
	}
	// A model-provided coordinate cannot fill unknown canonical marketplace data.
	if _, reason = locationMatch(domain.Location{}, domain.Location{Latitude: valuePointer(-6.2), Longitude: valuePointer(106.8)}, nearby, 50); reason != "NEEDS_LOCATION" {
		t.Fatalf("model coordinate trusted: %s", reason)
	}
}
func TestQueryNormalizationAndCaps(t *testing.T) {
	queries, invalid, truncated := prepareQueries([]SearchQuery{{Query: " Laptop CODING ", ItemIDs: []string{"a"}}, {Query: "coding laptop laptop", ItemIDs: []string{"b"}}, {Query: "unknown", ItemIDs: []string{"invented"}}, {Query: "camera", ItemIDs: []string{"a"}}}, map[string]bool{"a": true, "b": true}, 1)
	if len(queries) != 1 || queries[0].Query != "coding laptop" || strings.Join(queries[0].ItemIDs, ",") != "a,b" || !invalid || !truncated {
		t.Fatalf("normalization %+v invalid=%v truncated=%v", queries, invalid, truncated)
	}
}
