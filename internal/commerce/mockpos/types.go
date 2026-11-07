package mockpos

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// ReservedStock reads authoritative live holds on the caller's transaction.
// It must not acquire another connection or perform network I/O.
type ReservedStock func(context.Context, *sql.Tx, string, string, time.Time) (int, error)

// NoReservations is ONLY for standalone inventory before checkout exists.
// Replace it with authoritative reservation SQL before integrating checkout.
func NoReservations(context.Context, *sql.Tx, string, string, time.Time) (int, error) { return 0, nil }

type Options struct {
	Now           func() time.Time
	NewID         func() string
	ReservedStock ReservedStock
}
type Service struct {
	db       *sql.DB
	now      func() time.Time
	id       func() string
	reserved ReservedStock
}

func New(db *sql.DB, opts Options) (*Service, error) {
	if db == nil || opts.ReservedStock == nil {
		return nil, problem(contracts.InvalidArgument, "database and reservation reader are required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = func() string {
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				panic(err)
			}
			return hex.EncodeToString(b[:])
		}
	}
	return &Service{db: db, now: opts.Now, id: opts.NewID, reserved: opts.ReservedStock}, nil
}

type Item struct {
	ID             string            `json:"id"`
	SellerID       string            `json:"seller_id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Attributes     map[string]string `json:"attributes"`
	ListPriceIDR   int64             `json:"list_price_idr"`
	Currency       string            `json:"currency"`
	PhysicalStock  int               `json:"physical_stock"`
	ReservedStock  int               `json:"reserved_stock"`
	AvailableStock int               `json:"available_stock"`
	City           string            `json:"city"`
	Active         bool              `json:"active"`
	Version        int64             `json:"version"`
}
type CreateItem struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Attributes   map[string]string `json:"attributes"`
	ListPriceIDR int64             `json:"list_price_idr"`
	InitialStock int               `json:"initial_stock"`
	City         string            `json:"city"`
}
type PatchItem struct {
	ExpectedVersion int64              `json:"expected_version"`
	Name            *string            `json:"name,omitempty"`
	Description     *string            `json:"description,omitempty"`
	Attributes      *map[string]string `json:"attributes,omitempty"`
	ListPriceIDR    *int64             `json:"list_price_idr,omitempty"`
	City            *string            `json:"city,omitempty"`
	Active          *bool              `json:"active,omitempty"`
}
type AdjustStock struct {
	ExpectedVersion int64  `json:"expected_version"`
	Delta           int    `json:"delta"`
	Reason          string `json:"reason"`
}
type Policy struct {
	FloorPriceIDR      int64 `json:"floor_price_idr"`
	MaxDiscountBPS     int   `json:"max_discount_bps"`
	MessageCap         int   `json:"message_cap"`
	RateLimitPerMinute int   `json:"rate_limit_per_minute"`
	Version            int64 `json:"version"`
}
type PatchPolicy struct {
	ExpectedVersion    int64  `json:"expected_version"`
	FloorPriceIDR      *int64 `json:"floor_price_idr,omitempty"`
	MaxDiscountBPS     *int   `json:"max_discount_bps,omitempty"`
	MessageCap         *int   `json:"message_cap,omitempty"`
	RateLimitPerMinute *int   `json:"rate_limit_per_minute,omitempty"`
}
type Page struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func problem(code contracts.Code, message string) error {
	return &contracts.Error{Code: code, Message: message}
}
func (i Item) domainItem() domain.Item {
	return domain.Item{ID: i.ID, SellerID: i.SellerID, Name: i.Name, Description: i.Description, Attributes: i.Attributes, ListPriceIDR: i.ListPriceIDR, AvailableStock: i.AvailableStock, Location: domain.Location{City: i.City}, Active: i.Active, Version: i.Version}
}
