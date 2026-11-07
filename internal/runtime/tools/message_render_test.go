package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func renderFixture() (domain.LeadRecord, time.Time) {
	now := time.Date(2026, 11, 7, 4, 0, 0, 0, time.UTC)
	offer := &domain.Offer{ID: "offer-1", LeadID: "lead-1", ItemID: "item-1", PriceIDR: 5_600_000, Qty: 1}
	checkout := &domain.Checkout{ID: "checkout-1", LeadID: "lead-1", OfferID: offer.ID, ItemID: offer.ItemID, PriceIDR: offer.PriceIDR, TotalIDR: offer.PriceIDR, Qty: 1, Status: domain.CheckoutPending, ExpiresAt: now.Add(time.Minute), URL: "https://shop.example/checkout/private-token"}
	return domain.LeadRecord{ID: offer.LeadID, SellerID: "seller-1", ItemID: offer.ItemID, State: domain.LeadReserved, Offer: offer, Checkout: checkout}, now
}

func TestRenderMessageUsesStoredFacts(t *testing.T) {
	lead, now := renderFixture()
	got, err := RenderMessage("Harganya {price}. Bayar di {checkout_link}", lead, "https://shop.example", now)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Harganya Rp5.600.000. Bayar di "+lead.Checkout.URL {
		t.Fatalf("unexpected rendered message: %q", got)
	}
	// A non-commercial message does not need an offer or checkout.
	if got, err := RenderMessage("Halo!", domain.LeadRecord{}, "", time.Time{}); err != nil || got != "Halo!" {
		t.Fatalf("plain message=%q, error=%v", got, err)
	}
	// The configured origin may be a local HTTP server for the mock demo.
	lead.Checkout.URL = "http://127.0.0.1:8080/checkout/local-token"
	if _, err := RenderMessage("{checkout_link}", lead, "http://127.0.0.1:8080", now); err != nil {
		t.Fatal(err)
	}
}

func TestRenderMessageRejectsUnboundOrExpiredFacts(t *testing.T) {
	for _, tc := range []struct {
		name, template string
		mutate         func(*domain.LeadRecord, *time.Time)
	}{
		{"missing offer", "{price}", func(l *domain.LeadRecord, _ *time.Time) { l.Offer = nil }},
		{"wrong offer lead", "{price}", func(l *domain.LeadRecord, _ *time.Time) { l.Offer.LeadID = "other" }},
		{"wrong offer item", "{price}", func(l *domain.LeadRecord, _ *time.Time) { l.Offer.ItemID = "other" }},
		{"invalid quantity", "{price}", func(l *domain.LeadRecord, _ *time.Time) { l.Offer.Qty = 2 }},
		{"invalid price", "{price}", func(l *domain.LeadRecord, _ *time.Time) { l.Offer.PriceIDR = 0 }},
		{"missing checkout", "{checkout_link}", func(l *domain.LeadRecord, _ *time.Time) { l.Checkout = nil }},
		{"wrong checkout lead", "{checkout_link}", func(l *domain.LeadRecord, _ *time.Time) { l.Checkout.LeadID = "other" }},
		{"wrong checkout offer", "{checkout_link}", func(l *domain.LeadRecord, _ *time.Time) { l.Checkout.OfferID = "other" }},
		{"wrong checkout terms", "{checkout_link}", func(l *domain.LeadRecord, _ *time.Time) { l.Checkout.PriceIDR-- }},
		{"wrong checkout total", "{checkout_link}", func(l *domain.LeadRecord, _ *time.Time) { l.Checkout.TotalIDR-- }},
		{"expired at boundary", "{checkout_link}", func(l *domain.LeadRecord, n *time.Time) { l.Checkout.ExpiresAt = *n }},
		{"paid checkout", "{checkout_link}", func(l *domain.LeadRecord, _ *time.Time) { l.Checkout.Status = domain.CheckoutPaid }},
		{"not reserved", "{checkout_link}", func(l *domain.LeadRecord, _ *time.Time) { l.State = domain.LeadActive }},
		{"missing clock", "{checkout_link}", func(_ *domain.LeadRecord, n *time.Time) { *n = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lead, now := renderFixture()
			tc.mutate(&lead, &now)
			got, err := RenderMessage(tc.template, lead, "https://shop.example", now)
			if err == nil || got != "" {
				t.Fatalf("unvalidated text returned: %q, %v", got, err)
			}
			if strings.Contains(err.Error(), "private-token") {
				t.Fatal("error leaked checkout capability")
			}
		})
	}
}

func TestRenderMessageRejectsTemplatesAndModelDestinations(t *testing.T) {
	lead, now := renderFixture()
	for _, text := range []string{"{discount}", "{price", "{checkout_link}}", "Pay https://evil.example", "https://shop.example/checkout/private-token", "www.evil.example", "evil.example/pay", "pay@evil.example", "bitcoin://wallet", "bitcoin:wallet", "upi:account", "[pay](/checkout/other)", "<a href='/pay'>pay</a>"} {
		if got, err := RenderMessage(text, lead, "https://shop.example", now); err == nil || got != "" {
			t.Errorf("accepted model template/destination %q: %q", text, got)
		}
	}
}

func TestRenderMessageChecksOwnedCheckoutURL(t *testing.T) {
	for _, tc := range []struct{ url, origin string }{
		{"https://evil.example/checkout/token", "https://shop.example"},
		{"http://shop.example/checkout/token", "https://shop.example"},
		{"https://shop.example:8443/checkout/token", "https://shop.example"},
		{"https://user@shop.example/checkout/token", "https://shop.example"},
		{"/checkout/token", "https://shop.example"},
		{"https://shop.example/checkout/", "https://shop.example"},
		{"https://shop.example/checkout/a/b", "https://shop.example"},
		{"https://shop.example/checkout/token#fragment", "https://shop.example"},
		{"https://shop.example/checkout/token", "https://shop.example/base"},
		{"https://shop.example/checkout/token", ""},
	} {
		lead, now := renderFixture()
		lead.Checkout.URL = tc.url
		if got, err := RenderMessage("{checkout_link}", lead, tc.origin, now); err == nil || got != "" {
			t.Errorf("accepted checkout %q with origin %q", tc.url, tc.origin)
		}
	}
}
