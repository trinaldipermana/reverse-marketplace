package discovery

import (
	"context"
	"database/sql"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
)

type JSONGenerator interface {
	GenerateJSON(context.Context, llm.JSONRequest, any) error
}
type SearchQuery struct {
	Query   string   `json:"query"`
	ItemIDs []string `json:"item_ids"`
}
type QueryPlanner interface {
	PlanQueries(context.Context, []domain.Item, int) ([]SearchQuery, error)
}
type ItemMatch struct {
	ItemID string  `json:"item_id"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}
type PostMatch struct {
	Intent         domain.Intent
	BudgetCurrency string
	Matches        []ItemMatch
}
type PostMatcher interface {
	MatchPost(context.Context, domain.Post, []domain.Item) (PostMatch, error)
}
type LeadNotifier interface {
	OnLeadDiscovered(context.Context, string, string) error
}
type Commerce interface {
	GetItem(context.Context, string, string) (domain.Item, error)
	GetRules(context.Context, string, string) (domain.Rules, error)
	CreateLead(context.Context, commerce.CreateLeadInput) (domain.LeadRecord, bool, error)
}

type Options struct {
	// FixedPrice is opt-in; URL hosts are configured by the operator, not the model.
	FixedPrice       bool
	PartnerHosts     []string
	Now              func() time.Time
	NewID            func() string
	Timeout          time.Duration
	MaxQueries       int
	PageSize         int
	MaxPagesPerQuery int
	MaxPosts         int
	MaxItems         int
	IntentThreshold  float64
	MatchThreshold   float64
	MaxDistanceKM    float64
}
type Service struct {
	db          *sql.DB
	commerce    Commerce
	marketplace contracts.MarketplaceDiscovery
	planner     QueryPlanner
	matcher     PostMatcher
	notifier    LeadNotifier
	opts        Options
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type BuyerView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Initials string `json:"initials"`
	Source   string `json:"source"`
	Post     string `json:"post"`
	PostedAt string `json:"postedAt"`
}
type ProductView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Condition    string `json:"condition"`
	Category     string `json:"category"`
	Price        string `json:"price"`
	PriceIDR     int64  `json:"priceIDR"`
	Availability string `json:"availability"`
	Marketplace  string `json:"marketplace"`
	URL          string `json:"url"`
}
type MatchView struct {
	MatchID            string           `json:"matchId"`
	LeadID             string           `json:"leadId"`
	ThreadID           string           `json:"threadId"`
	MatchScore         int              `json:"matchScore"`
	MatchReasons       []string         `json:"matchReasons"`
	Buyer              BuyerView        `json:"buyer"`
	Product            ProductView      `json:"product"`
	SellerID           string           `json:"sellerId"`
	NotificationStatus string           `json:"notificationStatus"`
	LeadState          domain.LeadState `json:"leadState"`
}
type Result struct {
	RunID           string      `json:"runId"`
	RequestsScanned int         `json:"requestsScanned"`
	ListingsScanned int         `json:"listingsScanned"`
	Matches         []MatchView `json:"matches"`
	Warnings        []Warning   `json:"warnings"`
	Truncated       bool        `json:"truncated"`
}
