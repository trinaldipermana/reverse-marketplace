package discovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
)

// Exercise the actual model adapter before the hard geography gate, rather
// than supplying a handcrafted PostMatch which can conceal adapter data loss.
func TestModelAdapterCityEvidenceReachesDiscoveryConflictGate(t *testing.T) {
	for _, test := range []struct {
		name, source, extracted, wantCode string
		wantMatches                       int
	}{
		{"conflicting request", "Jakarta", "Bandung", "LOCATION_CONFLICT", 0},
		{"matching request", "Jakarta", "Jakarta", "", 1},
		{"known source no requested city", "Jakarta", "", "", 1},
		{"unknown source incompatible request", "", "Bandung", "LOCATION", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			post := pipelinePost("city-evidence", "Need coding laptop, requested city "+test.extracted, test.source)
			post.Location.Latitude = valuePointer(-6.2)
			post.Location.Longitude = valuePointer(106.8)
			model, err := NewModelAdapter(generatorFunc(func(_ context.Context, request llm.JSONRequest, out any) error {
				if request.SchemaName == "inventory_queries" {
					return json.Unmarshal([]byte(`{"queries":[{"query":"laptop","item_ids":["item_thinkpad"]}]}`), out)
				}
				var body map[string]any
				if err := json.Unmarshal([]byte(strings.Replace(validMatch, `"item_id":"laptop"`, `"item_id":"item_thinkpad"`, 1)), &body); err != nil {
					return err
				}
				body["quantity"] = 1
				body["city"] = nil
				if test.extracted != "" {
					body["city"] = test.extracted
				}
				raw, err := json.Marshal(body)
				if err != nil {
					return err
				}
				return json.Unmarshal(raw, out)
			}))
			if err != nil {
				t.Fatal(err)
			}
			notify := &notificationFake{}
			service, _, db, _ := setupPipeline(t, []domain.Post{post}, model, model, notify, Options{})
			result, err := service.Discover(context.Background(), mockpos.DemoSellerID)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Matches) != test.wantMatches {
				t.Fatalf("source=%q requested=%q matches=%d, want %d", test.source, test.extracted, len(result.Matches), test.wantMatches)
			}
			if got := pipelineCount(t, db, `SELECT count(*) FROM leads`); got != test.wantMatches {
				t.Fatalf("persisted leads=%d", got)
			}
			if got := pipelineCount(t, db, `SELECT count(*) FROM runtime_notifications`); got != test.wantMatches {
				t.Fatalf("persisted notifications=%d", got)
			}
			if got := len(notify.snapshot()); got != test.wantMatches {
				t.Fatalf("runtime enqueue count=%d", got)
			}
			if test.wantCode != "" {
				var code string
				if err = db.QueryRow(`SELECT decision_reason FROM discovery_matches`).Scan(&code); err != nil || code != test.wantCode {
					t.Fatalf("decision=%q err=%v", code, err)
				}
			}
			var intent string
			if err = db.QueryRow(`SELECT intent_json FROM discovery_posts`).Scan(&intent); err != nil {
				t.Fatal(err)
			}
			var evidence PostMatch
			if err = json.Unmarshal([]byte(intent), &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.Intent.Location.City != test.extracted {
				t.Fatalf("extracted city lost in evidence: %q", evidence.Intent.Location.City)
			}
		})
	}
}
