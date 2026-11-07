package commerce

import (
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// CheckoutView is the explicit buyer projection. It deliberately contains no
// token, checkout URL, seller policy, or private source data.
type CheckoutView struct {
	ID        string               `json:"id"`
	LeadID    string               `json:"lead_id"`
	ItemID    string               `json:"item_id"`
	ItemName  string               `json:"item_name"`
	PriceIDR  int64                `json:"price_idr"`
	Quantity  int                  `json:"quantity"`
	TotalIDR  int64                `json:"total_idr"`
	Currency  string               `json:"currency"`
	Status    domain.CheckoutState `json:"status"`
	ExpiresAt time.Time            `json:"expires_at"`
	Payment   *Payment             `json:"payment,omitempty"`
}

// Payment is a successful immutable mock receipt. Failed attempts create no row.
type Payment struct {
	ID         string    `json:"id"`
	CheckoutID string    `json:"checkout_id"`
	LeadID     string    `json:"lead_id"`
	AmountIDR  int64     `json:"amount_idr"`
	Quantity   int       `json:"quantity"`
	Currency   string    `json:"currency"`
	PaidAt     time.Time `json:"paid_at"`
}
