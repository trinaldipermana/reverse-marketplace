// Package commercefake provides configurable integration doubles, not an
// inventory or payment implementation. Configure callbacks before use. Calls
// may be concurrent; callback synchronization is the test author's responsibility.
package commercefake

import (
	"context"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// Fake lets P1 script success, business rejection, and service failure without
// connecting a database. An unconfigured operation fails instead of pretending
// that an offer, checkout, or transition succeeded.
type Fake struct {
	GetLeadFunc        func(context.Context, string) (domain.LeadRecord, error)
	GetItemFunc        func(context.Context, string, string) (domain.Item, error)
	GetRulesFunc       func(context.Context, string, string) (domain.Rules, error)
	RecordOfferFunc    func(context.Context, string, string, int64, int, string) (domain.Offer, error)
	CreateCheckoutFunc func(context.Context, string, string, string) (domain.Checkout, error)
	TransitionLeadFunc func(context.Context, string, domain.LeadState, string) (domain.LeadRecord, error)
}

var _ contracts.Commerce = (*Fake)(nil)

func unavailable(method string) error {
	return &contracts.Error{Code: contracts.DependencyUnavailable, Message: "fake " + method + " is not configured"}
}

func (f *Fake) GetLead(ctx context.Context, id string) (domain.LeadRecord, error) {
	if err := ctx.Err(); err != nil {
		return domain.LeadRecord{}, err
	}
	if f.GetLeadFunc == nil {
		return domain.LeadRecord{}, unavailable("GetLead")
	}
	return f.GetLeadFunc(ctx, id)
}

func (f *Fake) GetItem(ctx context.Context, sellerID, itemID string) (domain.Item, error) {
	if err := ctx.Err(); err != nil {
		return domain.Item{}, err
	}
	if f.GetItemFunc == nil {
		return domain.Item{}, unavailable("GetItem")
	}
	return f.GetItemFunc(ctx, sellerID, itemID)
}

func (f *Fake) GetRules(ctx context.Context, sellerID, itemID string) (domain.Rules, error) {
	if err := ctx.Err(); err != nil {
		return domain.Rules{}, err
	}
	if f.GetRulesFunc == nil {
		return domain.Rules{}, unavailable("GetRules")
	}
	return f.GetRulesFunc(ctx, sellerID, itemID)
}

func (f *Fake) RecordOffer(ctx context.Context, leadID, itemID string, priceIDR int64, qty int, key string) (domain.Offer, error) {
	if err := ctx.Err(); err != nil {
		return domain.Offer{}, err
	}
	if f.RecordOfferFunc == nil {
		return domain.Offer{}, unavailable("RecordOffer")
	}
	return f.RecordOfferFunc(ctx, leadID, itemID, priceIDR, qty, key)
}

func (f *Fake) CreateCheckout(ctx context.Context, leadID, offerID, key string) (domain.Checkout, error) {
	if err := ctx.Err(); err != nil {
		return domain.Checkout{}, err
	}
	if f.CreateCheckoutFunc == nil {
		return domain.Checkout{}, unavailable("CreateCheckout")
	}
	return f.CreateCheckoutFunc(ctx, leadID, offerID, key)
}

func (f *Fake) TransitionLead(ctx context.Context, leadID string, target domain.LeadState, reason string) (domain.LeadRecord, error) {
	if err := ctx.Err(); err != nil {
		return domain.LeadRecord{}, err
	}
	if f.TransitionLeadFunc == nil {
		return domain.LeadRecord{}, unavailable("TransitionLead")
	}
	return f.TransitionLeadFunc(ctx, leadID, target, reason)
}
