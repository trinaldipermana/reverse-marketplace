package contracts

import (
	"context"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// Commerce is the proposed P2 service boundary consumed by P1. See issue #8
// and docs/contracts/p2-commerce.md; this additive proposal is not wired yet.
// Implementations enforce policy and stock even if the caller checked earlier.
type Commerce interface {
	GetLead(ctx context.Context, leadID string) (domain.LeadRecord, error)
	GetItem(ctx context.Context, sellerID, itemID string) (domain.Item, error)
	GetRules(ctx context.Context, sellerID, itemID string) (domain.Rules, error)
	RecordOffer(ctx context.Context, leadID, itemID string, priceIDR int64, qty int, actionKey string) (domain.Offer, error)
	CreateCheckout(ctx context.Context, leadID, offerID, actionKey string) (domain.Checkout, error)
	// Only permitted lead transitions are accepted. Payment and expiry use
	// dedicated operations so they cannot bypass inventory transactions.
	// Replay metadata for this method remains under discussion in issue #8.
	TransitionLead(ctx context.Context, leadID string, target domain.LeadState, reason string) (domain.LeadRecord, error)
}
