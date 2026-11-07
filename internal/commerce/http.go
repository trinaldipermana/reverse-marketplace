package commerce

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// Handler combines P2 product APIs. Token checkout routes use bearer capability;
// inventory/policy/lead routes use the existing demo seller context. It does not
// implement marketplace messaging, discovery, or P1 manual conversation starts.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	inventory := s.Inventory().Handler()
	mux.Handle("/api/inventory", inventory)
	mux.Handle("/api/inventory/", inventory)
	mux.Handle("/api/seller/policy", inventory)
	mux.Handle("/api/checkouts/", s.CheckoutHandler())
	mux.HandleFunc("GET /api/leads", s.listLeadsHTTP)
	mux.HandleFunc("GET /api/leads/{id}", func(w http.ResponseWriter, r *http.Request) {
		seller := strings.TrimSpace(r.Header.Get("X-Seller-ID"))
		if seller == "" {
			productError(w, commerceProblem(contracts.InvalidArgument, "X-Seller-ID is required"))
			return
		}
		lead, err := s.GetSellerLead(r.Context(), seller, r.PathValue("id"))
		if err != nil {
			productError(w, err)
			return
		}
		productJSON(w, 200, map[string]any{"data": publicLead(lead)})
	})
	return mux
}

// Public lead projections intentionally omit bearer checkout URL and private
// seller policy; never serialize the trusted LeadRecord directly into an API.
type leadView struct {
	ID       string            `json:"id"`
	SellerID string            `json:"seller_id"`
	Platform string            `json:"platform"`
	PostID   string            `json:"post_id"`
	BuyerID  string            `json:"buyer_id"`
	ThreadID string            `json:"thread_id"`
	ItemID   string            `json:"item_id"`
	State    domain.LeadState  `json:"state"`
	Version  int64             `json:"version"`
	Intent   map[string]any    `json:"intent"`
	Offer    *offerView        `json:"offer"`
	Checkout *leadCheckoutView `json:"checkout"`
}
type offerView struct {
	ID        string    `json:"id"`
	ItemID    string    `json:"item_id"`
	PriceIDR  int64     `json:"price_idr"`
	Quantity  int       `json:"quantity"`
	CreatedAt time.Time `json:"created_at"`
}
type leadCheckoutView struct {
	ID        string               `json:"id"`
	Status    domain.CheckoutState `json:"status"`
	TotalIDR  int64                `json:"total_idr"`
	Quantity  int                  `json:"quantity"`
	ExpiresAt time.Time            `json:"expires_at"`
}

func publicLead(lead domain.LeadRecord) leadView {
	requirements := make([]map[string]string, 0, len(lead.Intent.Requirements))
	for _, r := range lead.Intent.Requirements {
		requirements = append(requirements, map[string]string{"name": r.Name, "value": r.Value})
	}
	out := leadView{ID: lead.ID, SellerID: lead.SellerID, Platform: lead.Platform, PostID: lead.PostID, BuyerID: lead.BuyerID, ThreadID: lead.ThreadID, ItemID: lead.ItemID, State: lead.State, Version: lead.Version, Intent: map[string]any{"item_query": lead.Intent.ItemQuery, "requirements": requirements, "quantity": lead.Intent.Qty, "city": lead.Intent.Location.City, "budget_idr": lead.Intent.BudgetIDR, "urgency_text": lead.Intent.UrgencyText, "needed_by": lead.Intent.NeededBy}}
	if lead.Offer != nil {
		o := lead.Offer
		out.Offer = &offerView{ID: o.ID, ItemID: o.ItemID, PriceIDR: o.PriceIDR, Quantity: o.Qty, CreatedAt: o.CreatedAt}
	}
	if lead.Checkout != nil {
		c := lead.Checkout
		out.Checkout = &leadCheckoutView{ID: c.ID, Status: c.Status, TotalIDR: c.TotalIDR, Quantity: c.Qty, ExpiresAt: c.ExpiresAt}
	}
	return out
}
func (s *Service) listLeadsHTTP(w http.ResponseWriter, r *http.Request) {
	seller := strings.TrimSpace(r.Header.Get("X-Seller-ID"))
	if seller == "" {
		productError(w, commerceProblem(contracts.InvalidArgument, "X-Seller-ID is required"))
		return
	}
	limit := 20
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			productError(w, commerceProblem(contracts.InvalidArgument, "limit must be between 1 and 100"))
			return
		}
	}
	after := ""
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(decoded) == 0 || len(decoded) > 200 {
			productError(w, commerceProblem(contracts.InvalidArgument, "invalid cursor"))
			return
		}
		after = string(decoded)
	}
	state := r.URL.Query().Get("state")
	if state != "" && state != "active" && state != "reserved" && state != "escalated" && state != "paid" && state != "lost" {
		productError(w, commerceProblem(contracts.InvalidArgument, "invalid lead state"))
		return
	}
	var count int
	if err = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM sellers WHERE id=?`, seller).Scan(&count); err != nil {
		productError(w, err)
		return
	}
	if count == 0 {
		productError(w, commerceProblem(contracts.NotFound, "seller not found"))
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id FROM leads WHERE seller_id=? AND id>? AND (?='' OR state=?) ORDER BY id LIMIT ?`, seller, after, state, state, limit+1)
	if err != nil {
		productError(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			productError(w, err)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		productError(w, err)
		return
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(ids[len(ids)-1]))
	}
	items := make([]leadView, 0, len(ids))
	for _, id := range ids {
		lead, err := s.GetSellerLead(r.Context(), seller, id)
		if err != nil {
			productError(w, err)
			return
		}
		items = append(items, publicLead(lead))
	}
	productJSON(w, 200, map[string]any{"data": map[string]any{"items": items, "next_cursor": next}})
}
func productJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func productError(w http.ResponseWriter, err error) {
	status := 503
	code := contracts.DependencyUnavailable
	message := "commerce service unavailable"
	retryable := true
	var failure *contracts.Error
	if errors.As(err, &failure) {
		code = failure.Code
		message = failure.Message
		retryable = failure.Retryable
		switch code {
		case contracts.NotFound:
			status = 404
		case contracts.InvalidArgument:
			status = 400
		case contracts.InvalidState, contracts.PriceBelowMinimum, contracts.OutOfStock, contracts.OfferSuperseded, contracts.CheckoutExpired, contracts.VersionConflict, contracts.IdempotencyConflict:
			status = 409
		}
	}
	productJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "retryable": retryable}})
}
