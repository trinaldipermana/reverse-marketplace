package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
)

// NewCommerceTools binds the five commerce wrappers to one trusted, mutable lead
// for one serialized turn. It does not deliver messages. The caller supplies a
// nonempty unique action identity for each mutation, stable when retrying that
// same operation; a tool name alone is not such an identity. No replay durability
// is provided here. The lead and callback must not be shared across concurrent turns.
func NewCommerceTools(service contracts.Commerce, lead *domain.LeadRecord, actionKey func(context.Context, string) (string, error)) ([]agentruntime.Tool, error) {
	if service == nil || lead == nil || strings.TrimSpace(lead.ID) == "" || strings.TrimSpace(lead.SellerID) == "" || actionKey == nil {
		return nil, errors.New("commerce tools require service, trusted lead and action identity")
	}
	bound := &commerceTools{service: service, lead: lead, actionKey: actionKey}
	result := make([]agentruntime.Tool, 0, 5)
	for _, name := range []string{"get_item", "make_offer", "create_checkout", "escalate_to_seller", "close_lead"} {
		result = append(result, &commerceTool{bound: bound, name: name})
	}
	return result, nil
}

type commerceTools struct {
	service   contracts.Commerce
	lead      *domain.LeadRecord
	actionKey func(context.Context, string) (string, error)
}

type commerceTool struct {
	bound *commerceTools
	name  string
}

func (t *commerceTool) Definition() llm.ToolDefinition {
	var description, schema string
	switch t.name {
	case "get_item":
		description = "Get current details and stock of one seller-owned item."
		schema = `{"type":"object","properties":{"item_id":{"type":"string"}},"required":["item_id"],"additionalProperties":false}`
	case "make_offer":
		description = "Record an offer in integer rupiah. Does not message the buyer; use send_message with {price} afterward."
		schema = `{"type":"object","properties":{"item_id":{"type":"string"},"price":{"type":"integer"}},"required":["item_id","price"],"additionalProperties":false}`
	case "create_checkout":
		description = "The buyer accepted the stored offer. Reserve its item and create checkout; then send_message with {checkout_link}."
		schema = `{"type":"object","properties":{},"required":[],"additionalProperties":false}`
	case "escalate_to_seller":
		description = "Request seller attention and end the turn."
		schema = `{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"],"additionalProperties":false}`
	case "close_lead":
		description = "Close the lead when the buyer is no longer interested and end the turn."
		schema = `{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"],"additionalProperties":false}`
	}
	return llm.ToolDefinition{Name: t.name, Description: description, Schema: json.RawMessage(schema)}
}

func (t *commerceTool) Run(ctx context.Context, raw json.RawMessage) (agentruntime.ToolOutcome, error) {
	if err := ctx.Err(); err != nil {
		return agentruntime.ToolOutcome{}, err
	}
	args, ok := parseCommerceArgs(t.name, raw)
	if !ok {
		return commerceBlocked(contracts.InvalidArgument), nil
	}
	b := t.bound
	if b.lead.State != domain.LeadActive {
		return commerceBlocked(contracts.InvalidState), nil
	}
	switch t.name {
	case "get_item":
		item, err := b.service.GetItem(ctx, b.lead.SellerID, args.itemID)
		if err != nil {
			return commerceFailure(ctx, err)
		}
		if item.ID != args.itemID || item.SellerID != b.lead.SellerID {
			return commerceBlocked(contracts.NotFound), nil
		}
		// Explicit model projection: never serialize rules or a full lead.
		return commerceResult(map[string]any{"item_id": item.ID, "name": item.Name, "description": item.Description, "attributes": item.Attributes, "list_price_idr": item.ListPriceIDR, "available_stock": item.AvailableStock, "active": item.Active, "city": item.Location.City}), nil
	case "make_offer":
		item, err := b.service.GetItem(ctx, b.lead.SellerID, args.itemID)
		if err != nil {
			return commerceFailure(ctx, err)
		}
		if item.ID != args.itemID || item.SellerID != b.lead.SellerID {
			return commerceBlocked(contracts.NotFound), nil
		}
		if !item.Active {
			return commerceBlocked(contracts.InvalidState), nil
		}
		if item.AvailableStock < 1 {
			return commerceBlocked(contracts.OutOfStock), nil
		}
		key, err := b.key(ctx, t.name)
		if err != nil {
			return agentruntime.ToolOutcome{}, err
		}
		// P2 authoritatively validates current effective basis-point policy.
		// Do not convert it to the separate integer-percent helper.
		offer, err := b.service.RecordOffer(ctx, b.lead.ID, args.itemID, args.price, 1, key)
		if err != nil {
			return commerceFailure(ctx, err)
		}
		if offer.ID == "" || offer.LeadID != b.lead.ID || offer.ItemID != args.itemID || offer.PriceIDR != args.price || offer.Qty != 1 {
			return agentruntime.ToolOutcome{}, errors.New("commerce returned inconsistent offer")
		}
		b.lead.ItemID = offer.ItemID
		b.lead.Offer = &offer
		return commerceResult(map[string]any{"status": "offer_recorded", "offer_id": offer.ID, "item_id": offer.ItemID, "price_idr": offer.PriceIDR, "qty": offer.Qty}), nil
	case "create_checkout":
		offer := b.lead.Offer
		if offer == nil || offer.ID == "" || offer.LeadID != b.lead.ID || offer.ItemID != b.lead.ItemID || offer.PriceIDR <= 0 || offer.Qty != 1 {
			return commerceBlocked(contracts.InvalidState), nil
		}
		key, err := b.key(ctx, t.name)
		if err != nil {
			return agentruntime.ToolOutcome{}, err
		}
		checkout, err := b.service.CreateCheckout(ctx, b.lead.ID, offer.ID, key)
		if err != nil {
			return commerceFailure(ctx, err)
		}
		if checkout.ID == "" || checkout.LeadID != b.lead.ID || checkout.OfferID != offer.ID || checkout.ItemID != offer.ItemID || checkout.PriceIDR != offer.PriceIDR || checkout.TotalIDR != offer.PriceIDR || checkout.Qty != 1 || checkout.Status != domain.CheckoutPending || checkout.URL == "" {
			return agentruntime.ToolOutcome{}, errors.New("commerce returned inconsistent checkout")
		}
		b.lead.Checkout = &checkout
		b.lead.State = domain.LeadReserved
		// Bearer URL stays only in trusted context for send_message rendering.
		return commerceResult(map[string]any{"status": "checkout_created", "checkout_id": checkout.ID}), nil
	case "escalate_to_seller", "close_lead":
		target := domain.LeadEscalated
		if t.name == "close_lead" {
			target = domain.LeadLost
		}
		updated, err := b.service.TransitionLead(ctx, b.lead.ID, target, args.reason)
		if err != nil {
			return commerceFailure(ctx, err)
		}
		if updated.ID != b.lead.ID || updated.SellerID != b.lead.SellerID || updated.State != target {
			return agentruntime.ToolOutcome{}, errors.New("commerce returned inconsistent lead transition")
		}
		*b.lead = updated
		out := commerceResult(map[string]any{"status": "lead_transitioned", "state": target})
		out.Stop = true
		return out, nil
	}
	return commerceBlocked(contracts.InvalidArgument), nil
}

func (b *commerceTools) key(ctx context.Context, name string) (string, error) {
	key, err := b.actionKey(ctx, name)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("commerce action identity unavailable")
	}
	if strings.TrimSpace(key) == "" {
		return "", errors.New("commerce action identity is required")
	}
	return key, nil
}

type commerceArgs struct {
	itemID string
	price  int64
	reason string
}

func parseCommerceArgs(name string, raw json.RawMessage) (commerceArgs, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
		return commerceArgs{}, false
	}
	// encoding/json's struct decoder matches keys case-insensitively; enforce
	// the exact model schema before decoding values.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return commerceArgs{}, false
	}
	var required []string
	switch name {
	case "get_item":
		required = []string{"item_id"}
	case "make_offer":
		required = []string{"item_id", "price"}
	case "create_checkout":
	case "escalate_to_seller", "close_lead":
		required = []string{"reason"}
	default:
		return commerceArgs{}, false
	}
	if len(fields) != len(required) {
		return commerceArgs{}, false
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return commerceArgs{}, false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	switch name {
	case "get_item":
		var v struct {
			ItemID *string `json:"item_id"`
		}
		if decoder.Decode(&v) != nil || v.ItemID == nil || strings.TrimSpace(*v.ItemID) == "" {
			return commerceArgs{}, false
		}
		return commerceArgs{itemID: *v.ItemID}, true
	case "make_offer":
		var v struct {
			ItemID *string `json:"item_id"`
			Price  *int64  `json:"price"`
		}
		if decoder.Decode(&v) != nil || v.ItemID == nil || strings.TrimSpace(*v.ItemID) == "" || v.Price == nil || *v.Price <= 0 {
			return commerceArgs{}, false
		}
		return commerceArgs{itemID: *v.ItemID, price: *v.Price}, true
	case "create_checkout":
		var v struct{}
		return commerceArgs{}, decoder.Decode(&v) == nil
	case "escalate_to_seller", "close_lead":
		var v struct {
			Reason *string `json:"reason"`
		}
		if decoder.Decode(&v) != nil || v.Reason == nil || strings.TrimSpace(*v.Reason) == "" {
			return commerceArgs{}, false
		}
		return commerceArgs{reason: *v.Reason}, true
	}
	return commerceArgs{}, false
}

func commerceResult(value any) agentruntime.ToolOutcome {
	raw, _ := json.Marshal(value)
	return agentruntime.ToolOutcome{Output: string(raw)}
}
func commerceBlocked(code contracts.Code) agentruntime.ToolOutcome {
	return commerceResult(map[string]contracts.Code{"error": code})
}

func commerceFailure(ctx context.Context, err error) (agentruntime.ToolOutcome, error) {
	if ctx.Err() != nil {
		return agentruntime.ToolOutcome{}, ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return agentruntime.ToolOutcome{}, context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return agentruntime.ToolOutcome{}, context.DeadlineExceeded
	}
	var failure *contracts.Error
	if errors.As(err, &failure) {
		switch failure.Code {
		case contracts.NotFound, contracts.InvalidArgument, contracts.InvalidState, contracts.PriceBelowMinimum, contracts.OutOfStock, contracts.OfferSuperseded, contracts.CheckoutExpired, contracts.VersionConflict, contracts.IdempotencyConflict:
			return commerceBlocked(failure.Code), nil
		}
	}
	return agentruntime.ToolOutcome{}, errors.New("commerce dependency failed")
}
