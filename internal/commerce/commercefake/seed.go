package commercefake

import (
	"context"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

const (
	SellerID = "seller_demo"
	ItemID   = "item_thinkpad"
	LeadID   = "lead_demo"
)

// NewSeeded returns isolated read fixtures for a Jakarta laptop lead. Writes
// remain unconfigured: callers must script them explicitly, not mistake a fake
// for working stock, policy enforcement, reservations, or payment.
func NewSeeded() *Fake {
	return &Fake{
		GetLeadFunc: func(_ context.Context, id string) (domain.LeadRecord, error) {
			if id != LeadID {
				return domain.LeadRecord{}, notFound()
			}
			budget := int64(6_000_000)
			return domain.LeadRecord{
				ID: LeadID, SellerID: SellerID, ItemID: ItemID,
				Platform: "mock", PostID: "post_demo", BuyerID: "buyer_demo", ThreadID: "thread_demo",
				State: domain.LeadActive, Version: 1,
				Intent: domain.Intent{
					IsBuyingRequest: true, Confidence: 0.96, ItemQuery: "used laptop for coding",
					Requirements: []domain.Requirement{{Name: "ram_gb_min", Value: "16"}},
					Qty:          1, Location: domain.Location{City: "Jakarta"}, BudgetIDR: &budget,
					UrgencyText: "this week",
				},
			}, nil
		},
		GetItemFunc: func(_ context.Context, sellerID, itemID string) (domain.Item, error) {
			if sellerID != SellerID || itemID != ItemID {
				return domain.Item{}, notFound()
			}
			return domain.Item{
				ID: ItemID, SellerID: SellerID, Name: "Used ThinkPad T480",
				Description:  "16GB RAM, 512GB SSD",
				Attributes:   map[string]string{"ram_gb": "16", "storage_gb": "512", "condition": "used"},
				ListPriceIDR: 5_800_000, AvailableStock: 1, Location: domain.Location{City: "Jakarta"},
				Active: true, Version: 1,
			}, nil
		},
		GetRulesFunc: func(_ context.Context, sellerID, itemID string) (domain.Rules, error) {
			if sellerID != SellerID || itemID != ItemID {
				return domain.Rules{}, notFound()
			}
			return domain.Rules{FloorPriceIDR: 5_400_000, MaxDiscountBPS: 1000, MessageCap: 10, RateLimitPerMinute: 20, Version: 1}, nil
		},
	}
}

func notFound() error {
	return &contracts.Error{Code: contracts.NotFound, Message: "resource not found"}
}
