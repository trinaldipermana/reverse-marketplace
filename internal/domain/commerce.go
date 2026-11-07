package domain

import "time"

// LeadState is persisted and controlled by commerce services, never model text.
type LeadState string

const (
	LeadActive    LeadState = "active"
	LeadReserved  LeadState = "reserved"
	LeadEscalated LeadState = "escalated"
	LeadPaid      LeadState = "paid"
	LeadLost      LeadState = "lost"
)

type CheckoutState string

const (
	CheckoutPending   CheckoutState = "pending"
	CheckoutPaid      CheckoutState = "paid"
	CheckoutExpired   CheckoutState = "expired"
	CheckoutCancelled CheckoutState = "cancelled"
)

// Location preserves unknown coordinates rather than manufacturing a distance.
type Location struct {
	City      string
	Latitude  *float64
	Longitude *float64
}

type Requirement struct {
	Name  string
	Value string
}

// Intent is a validated buying-request snapshot. Unknown budget/deadline is nil.
type Intent struct {
	IsBuyingRequest bool
	Confidence      float64
	ItemQuery       string
	Requirements    []Requirement
	Qty             int
	Location        Location
	BudgetIDR       *int64
	UrgencyText     string
	NeededBy        *time.Time
}

// Item is the seller-scoped inventory view. AvailableStock excludes live holds.
type Item struct {
	ID             string
	SellerID       string
	Name           string
	Description    string
	Attributes     map[string]string
	ListPriceIDR   int64
	AvailableStock int
	Location       Location
	Active         bool
	Version        int64
}

// Rules is server-only effective policy. Do not use it as a prompt/API payload.
// Private pricing fields are excluded from JSON as defense against accidental
// serialization; public API handlers must still use explicit safe projections.
type Rules struct {
	FloorPriceIDR      int64 `json:"-"`
	MaxDiscountBPS     int   `json:"-"`
	MessageCap         int
	RateLimitPerMinute int
	Version            int64
}

// Offer terms are immutable, integer IDR, and Qty is one in the MVP.
type Offer struct {
	ID        string
	LeadID    string
	ItemID    string
	PriceIDR  int64
	Qty       int
	CreatedAt time.Time
}

// Checkout terms come only from a persisted offer. URL is a bearer capability
// for delivery to the buyer; exclude it from ordinary logs and public lead views.
type Checkout struct {
	ID        string
	LeadID    string
	OfferID   string
	ItemID    string
	PriceIDR  int64
	Qty       int
	TotalIDR  int64
	URL       string `json:"-"`
	Status    CheckoutState
	ExpiresAt time.Time
}

// LeadRecord is trusted application context, not a public dashboard response.
// Offer and Checkout are nil until those records exist.
type LeadRecord struct {
	ID       string
	SellerID string
	Platform string
	PostID   string
	BuyerID  string
	ThreadID string
	ItemID   string
	State    LeadState
	Intent   Intent
	Offer    *Offer
	Checkout *Checkout
	Version  int64
}
