package discovery

import (
	"context"
	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"testing"
)

func TestFixedPriceRejectsDiscountOnlyMatchAndProjectsTrustedURL(t *testing.T) {
	ctx := context.Background()
	s, _, db, _ := setupPipeline(t, nil, nil, nil, nil, Options{FixedPrice: true, PartnerHosts: []string{"partner.example"}})
	const destination = "https://partner.example/listing/thinkpad?ref=seller%2Bdemo"
	_, err := db.Exec(`UPDATE inventory_items SET attributes_json=json_set(attributes_json,'$.partner_listing_url',?) WHERE id='item_thinkpad'`, destination)
	if err != nil {
		t.Fatal(err)
	}
	post := pipelinePost("buy", "Need coding laptop", "Jakarta")
	matched, _ := pipelineMatch(ctx, post, nil)
	candidate := ItemMatch{ItemID: "item_thinkpad", Score: .95, Reason: "RAM match"}
	for _, test := range []struct {
		budget int64
		reason string
	}{{5500000, "BUDGET"}, {5800000, ""}, {6000000, ""}} {
		matched.Intent.BudgetIDR = &test.budget
		got, reason, err := s.validateCandidate(ctx, mockpos.DemoSellerID, post, matched, candidate, map[string]bool{"item_thinkpad": true})
		if err != nil || reason != test.reason {
			t.Fatalf("budget %d reason %s err %v", test.budget, reason, err)
		}
		if reason == "" && got.PartnerURL != destination {
			t.Fatal("lost trusted URL")
		}
	}
	_, err = db.Exec(`UPDATE inventory_items SET attributes_json=json_remove(attributes_json,'$.partner_listing_url') WHERE id='item_thinkpad'`)
	if err != nil {
		t.Fatal(err)
	}
	_, reason, err := s.validateCandidate(ctx, mockpos.DemoSellerID, post, matched, candidate, map[string]bool{"item_thinkpad": true})
	if err != nil || reason != "PARTNER_LISTING" {
		t.Fatalf("missing URL %s %v", reason, err)
	}
}

func TestFixedPriceDiscoveryUsesOneLeadWithoutCheckoutEffects(t *testing.T) {
	ctx := context.Background()
	s, _, db, _ := setupPipeline(t, []domain.Post{pipelinePost("buy", "Need coding laptop 16GB under 6 juta", "Jakarta")}, nil, nil, nil, Options{FixedPrice: true, PartnerHosts: []string{"partner.example"}})
	// Both 16 GB laptops have valid destinations: Dell must fail on fixed price,
	// even though the default negotiable policy would fit the buyer's budget.
	_, err := db.Exec(`UPDATE inventory_items SET attributes_json=json_set(attributes_json,'$.partner_listing_url','https://partner.example/listing/' || id || '?ref=demo')`)
	if err != nil {
		t.Fatal(err)
	}
	var firstLead string
	for i := 0; i < 2; i++ {
		result, err := s.Discover(ctx, mockpos.DemoSellerID)
		if err != nil || len(result.Matches) != 1 {
			t.Fatalf("%+v %v", result, err)
		}
		m := result.Matches[0]
		if m.Product.ID != "item_thinkpad" || m.Product.URL != "https://partner.example/listing/item_thinkpad?ref=demo" || m.Product.PriceIDR != 5800000 {
			t.Fatalf("%+v", m)
		}
		if i == 0 {
			firstLead = m.LeadID
		} else if firstLead != m.LeadID {
			t.Fatal("duplicate lead")
		}
	}
	for _, table := range []string{"reservations", "checkouts", "offers"} {
		if n := pipelineCount(t, db, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatalf("unexpected %s mutation", table)
		}
	}
	if n := pipelineCount(t, db, `SELECT physical_stock FROM inventory_items WHERE id='item_thinkpad'`); n != 2 {
		t.Fatal("stock changed")
	}
}
