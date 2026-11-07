package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/commercefake"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
)

func commerceFixture(t *testing.T) (*commercefake.Fake, *domain.LeadRecord, map[string]agentruntime.Tool) {
	t.Helper()
	fake := commercefake.NewSeeded()
	lead, err := fake.GetLead(context.Background(), commercefake.LeadID)
	if err != nil {
		t.Fatal(err)
	}
	wrappers, err := NewCommerceTools(fake, &lead, func(_ context.Context, name string) (string, error) { return "trusted-event-action-" + name, nil })
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]agentruntime.Tool{}
	for _, tool := range wrappers {
		byName[tool.Definition().Name] = tool
	}
	return fake, &lead, byName
}

func TestCommerceOfferUsesAuthoritativeService(t *testing.T) {
	fake, lead, wrappers := commerceFixture(t)
	if len(wrappers) != 5 {
		t.Fatal("expected five commerce tools")
	}
	for _, tool := range wrappers {
		var schema struct {
			Required             []string       `json:"required"`
			AdditionalProperties bool           `json:"additionalProperties"`
			Properties           map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(tool.Definition().Schema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.AdditionalProperties || len(schema.Required) != len(schema.Properties) {
			t.Fatal("tool schema is not strict")
		}
	}
	calls := 0
	fake.RecordOfferFunc = func(_ context.Context, leadID, itemID string, price int64, qty int, key string) (domain.Offer, error) {
		calls++
		if leadID != lead.ID || itemID != lead.ItemID || price != 5_600_000 || qty != 1 || key != "trusted-event-action-make_offer" {
			t.Fatal("incorrect authoritative call")
		}
		return domain.Offer{ID: "offer_1", LeadID: leadID, ItemID: itemID, PriceIDR: price, Qty: qty}, nil
	}
	result, err := wrappers["make_offer"].Run(context.Background(), json.RawMessage(`{"item_id":"item_thinkpad","price":5600000}`))
	if err != nil || calls != 1 || lead.Offer == nil || lead.Offer.ID != "offer_1" || result.Stop {
		t.Fatalf("offer failed: %+v, %v", result, err)
	}
	fake.RecordOfferFunc = func(context.Context, string, string, int64, int, string) (domain.Offer, error) {
		return domain.Offer{}, &contracts.Error{Code: contracts.PriceBelowMinimum, Message: "private floor 5400000", Cause: errors.New("secret cause")}
	}
	blocked, err := wrappers["make_offer"].Run(context.Background(), json.RawMessage(`{"item_id":"item_thinkpad","price":5000000}`))
	if err != nil || !strings.Contains(blocked.Output, "PRICE_BELOW_MINIMUM") || strings.Contains(blocked.Output, "5400000") || strings.Contains(blocked.Output, "secret") || lead.Offer.ID != "offer_1" {
		t.Fatalf("unsafe rejection: %+v, %v", blocked, err)
	}
}

func TestCommerceRejectsMalformedAndWrongSellerBeforeMutation(t *testing.T) {
	fake, lead, wrappers := commerceFixture(t)
	lookups, mutations := 0, 0
	fake.GetItemFunc = func(context.Context, string, string) (domain.Item, error) {
		lookups++
		return domain.Item{ID: lead.ItemID, SellerID: "other_seller", Active: true, AvailableStock: 1, ListPriceIDR: 5_800_000}, nil
	}
	fake.RecordOfferFunc = func(context.Context, string, string, int64, int, string) (domain.Offer, error) {
		mutations++
		return domain.Offer{}, nil
	}
	for _, args := range []string{`null`, `[]`, `{}`, `{"item_id":null,"price":1}`, `{"item_id":"item_thinkpad","price":"1"}`, `{"item_id":"item_thinkpad","price":1,"seller_id":"other"}`, `{"item_id":"item_thinkpad","price":1.5}`, `{"ITEM_ID":"item_thinkpad","price":1}`} {
		out, err := wrappers["make_offer"].Run(context.Background(), json.RawMessage(args))
		if err != nil || !strings.Contains(out.Output, "INVALID_ARGUMENT") {
			t.Fatalf("arguments accepted: %s, %+v, %v", args, out, err)
		}
	}
	if lookups != 0 || mutations != 0 {
		t.Fatal("malformed arguments reached commerce")
	}
	out, err := wrappers["make_offer"].Run(context.Background(), json.RawMessage(`{"item_id":"item_thinkpad","price":5600000}`))
	if err != nil || !strings.Contains(out.Output, "NOT_FOUND") || lookups != 1 || mutations != 0 {
		t.Fatalf("wrong seller mutated: %+v, %v", out, err)
	}
}

func TestCommerceCheckoutUsesStoredOfferAndKeepsURLPrivate(t *testing.T) {
	fake, lead, wrappers := commerceFixture(t)
	lead.Offer = &domain.Offer{ID: "offer_1", LeadID: lead.ID, ItemID: lead.ItemID, PriceIDR: 5_600_000, Qty: 1}
	calls := 0
	fake.CreateCheckoutFunc = func(_ context.Context, leadID, offerID, key string) (domain.Checkout, error) {
		calls++
		if leadID != lead.ID || offerID != "offer_1" || key != "trusted-event-action-create_checkout" {
			t.Fatal("checkout terms not from context")
		}
		return domain.Checkout{ID: "checkout_1", LeadID: leadID, OfferID: offerID, ItemID: lead.ItemID, PriceIDR: 5_600_000, TotalIDR: 5_600_000, Qty: 1, URL: "https://checkout.example/secret-token", Status: domain.CheckoutPending, ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
	}
	invalid, err := wrappers["create_checkout"].Run(context.Background(), json.RawMessage(`{"price":1}`))
	if err != nil || !strings.Contains(invalid.Output, "INVALID_ARGUMENT") || calls != 0 {
		t.Fatal("checkout model arguments accepted")
	}
	out, err := wrappers["create_checkout"].Run(context.Background(), json.RawMessage(`{}`))
	if err != nil || calls != 1 || lead.State != domain.LeadReserved || lead.Checkout == nil || lead.Checkout.URL == "" || out.Stop {
		t.Fatalf("checkout failed: %+v, %v", out, err)
	}
	if strings.Contains(out.Output, "secret-token") || strings.Contains(out.Output, "https://") {
		t.Fatal("checkout capability leaked into tool result")
	}
	fake.RecordOfferFunc = func(context.Context, string, string, int64, int, string) (domain.Offer, error) {
		t.Fatal("negotiation continued after reservation")
		return domain.Offer{}, nil
	}
	out, err = wrappers["make_offer"].Run(context.Background(), json.RawMessage(`{"item_id":"item_thinkpad","price":5600000}`))
	if err != nil || !strings.Contains(out.Output, "INVALID_STATE") {
		t.Fatal("reserved lead allowed negotiation")
	}
}

func TestCommerceTerminalTransitionsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target domain.LeadState
	}{{"escalate_to_seller", domain.LeadEscalated}, {"close_lead", domain.LeadLost}} {
		t.Run(tc.name, func(t *testing.T) {
			fake, lead, wrappers := commerceFixture(t)
			fake.TransitionLeadFunc = func(_ context.Context, id string, target domain.LeadState, reason string) (domain.LeadRecord, error) {
				if id != lead.ID || target != tc.target || reason != "buyer requested help" {
					t.Fatal("incorrect transition")
				}
				updated := *lead
				updated.State = target
				return updated, nil
			}
			out, err := wrappers[tc.name].Run(context.Background(), json.RawMessage(`{"reason":"buyer requested help"}`))
			if err != nil || !out.Stop || lead.State != tc.target {
				t.Fatalf("terminal action failed: %+v, %v", out, err)
			}
		})
	}
	fake, _, wrappers := commerceFixture(t)
	fake.GetItemFunc = func(context.Context, string, string) (domain.Item, error) {
		return domain.Item{}, &contracts.Error{Code: contracts.DependencyUnavailable, Message: "private service detail", Cause: errors.New("private cause")}
	}
	_, err := wrappers["get_item"].Run(context.Background(), json.RawMessage(`{"item_id":"item_thinkpad"}`))
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe service error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = wrappers["get_item"].Run(ctx, json.RawMessage(`{"item_id":"item_thinkpad"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
}
