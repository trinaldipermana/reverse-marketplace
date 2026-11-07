package commercefake_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/commercefake"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func operations(f *commercefake.Fake) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"GetLead":  func(ctx context.Context) error { _, err := f.GetLead(ctx, "lead"); return err },
		"GetItem":  func(ctx context.Context) error { _, err := f.GetItem(ctx, "seller", "item"); return err },
		"GetRules": func(ctx context.Context) error { _, err := f.GetRules(ctx, "seller", "item"); return err },
		"RecordOffer": func(ctx context.Context) error {
			_, err := f.RecordOffer(ctx, "lead", "item", 123, 1, "offer-key")
			return err
		},
		"CreateCheckout": func(ctx context.Context) error {
			_, err := f.CreateCheckout(ctx, "lead", "offer", "checkout-key")
			return err
		},
		"TransitionLead": func(ctx context.Context) error {
			_, err := f.TransitionLead(ctx, "lead", domain.LeadEscalated, "reason")
			return err
		},
	}
}

func TestUnconfiguredAndCancelledCallsFail(t *testing.T) {
	f := &commercefake.Fake{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, call := range operations(f) {
		t.Run(name, func(t *testing.T) {
			var failure *contracts.Error
			if err := call(context.Background()); !errors.As(err, &failure) || failure.Code != contracts.DependencyUnavailable || failure.Retryable {
				t.Fatalf("missing configuration must not pretend success or invite retries: %v", err)
			}
			if err := call(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func TestCallbacksReceiveContextAndArguments(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "trace")
	var got []any
	capture := func(c context.Context, args ...any) {
		if c.Value(key{}) != "trace" {
			t.Fatal("context was not forwarded")
		}
		got = args
	}
	blocked := &contracts.Error{Code: contracts.PriceBelowMinimum, Message: "below the seller's minimum"}
	f := &commercefake.Fake{
		GetLeadFunc: func(c context.Context, id string) (domain.LeadRecord, error) {
			capture(c, id)
			return domain.LeadRecord{ID: id}, nil
		},
		GetItemFunc: func(c context.Context, s, i string) (domain.Item, error) {
			capture(c, s, i)
			return domain.Item{ID: i}, nil
		},
		GetRulesFunc: func(c context.Context, s, i string) (domain.Rules, error) {
			capture(c, s, i)
			return domain.Rules{Version: 2}, nil
		},
		RecordOfferFunc: func(c context.Context, l, i string, p int64, q int, k string) (domain.Offer, error) {
			capture(c, l, i, p, q, k)
			return domain.Offer{}, blocked
		},
		CreateCheckoutFunc: func(c context.Context, l, o, k string) (domain.Checkout, error) {
			capture(c, l, o, k)
			return domain.Checkout{OfferID: o}, nil
		},
		TransitionLeadFunc: func(c context.Context, l string, s domain.LeadState, r string) (domain.LeadRecord, error) {
			capture(c, l, s, r)
			return domain.LeadRecord{State: s}, nil
		},
	}
	expected := map[string][]any{
		"GetLead": {"lead"}, "GetItem": {"seller", "item"}, "GetRules": {"seller", "item"},
		"RecordOffer":    {"lead", "item", int64(123), 1, "offer-key"},
		"CreateCheckout": {"lead", "offer", "checkout-key"}, "TransitionLead": {"lead", domain.LeadEscalated, "reason"},
	}
	for name, call := range operations(f) {
		t.Run(name, func(t *testing.T) {
			got = nil
			err := call(ctx)
			if !reflect.DeepEqual(got, expected[name]) {
				t.Fatalf("got %v, want %v", got, expected[name])
			}
			if name == "RecordOffer" {
				if err != blocked {
					t.Fatalf("business error changed: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	// A configured callback must not execute when the request was cancelled.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	got = nil
	for name, call := range operations(f) {
		if err := call(cancelled); !errors.Is(err, context.Canceled) || got != nil {
			t.Fatalf("%s called callback after cancellation: %v", name, err)
		}
	}
}

func TestSeededContextIsScopedAndIsolated(t *testing.T) {
	f := commercefake.NewSeeded()
	ctx := context.Background()
	lead, err := f.GetLead(ctx, commercefake.LeadID)
	if err != nil || lead.State != domain.LeadActive || lead.Offer != nil || lead.Checkout != nil {
		t.Fatalf("unexpected seed: %+v %v", lead, err)
	}
	item, err := f.GetItem(ctx, lead.SellerID, lead.ItemID)
	if err != nil || item.AvailableStock != 1 {
		t.Fatalf("unexpected item: %+v %v", item, err)
	}
	item.Attributes["ram_gb"] = "corrupted"
	*lead.Intent.BudgetIDR = 0
	again, _ := f.GetItem(ctx, lead.SellerID, lead.ItemID)
	leadAgain, _ := f.GetLead(ctx, lead.ID)
	if again.Attributes["ram_gb"] != "16" || *leadAgain.Intent.BudgetIDR != 6_000_000 {
		t.Fatal("fixture mutation escaped to later reads")
	}
	_, err = f.GetItem(ctx, "other-seller", lead.ItemID)
	var failure *contracts.Error
	if !errors.As(err, &failure) || failure.Code != contracts.NotFound {
		t.Fatalf("cross-seller lookup was not hidden: %v", err)
	}
	_, err = f.RecordOffer(ctx, lead.ID, lead.ItemID, 5_800_000, 1, "key")
	if !errors.As(err, &failure) || failure.Code != contracts.DependencyUnavailable {
		t.Fatalf("fixture pretended to persist an offer: %v", err)
	}
}
