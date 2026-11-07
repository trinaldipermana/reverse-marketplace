package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
)

// AgentSendFunc accepts the existing P4 owner request/result types. Pass
// marketplace.AgentSender.SendAgentMessage directly; no parallel contract is defined.
type AgentSendFunc func(context.Context, contracts.AgentMessageRequest) (contracts.AgentDeliveryResult, error)

type SendMessageOptions struct {
	CheckoutOrigin string
	Channel        contracts.AgentDeliveryChannel
	Now            func() time.Time
}

var (
	ErrSendMessagePending = errors.New("agent message publication is pending")
	ErrSendMessageFailed  = errors.New("agent message delivery could not be confirmed")
)

// NewSendMessageTool binds send_message(text) to one serialized turn. Construct
// it before executing tools and share the lead with the commerce wrappers. Only
// an active-to-reserved checkout newly observed after construction can be sent.
// P4 owns final validation, rendering, publication and same-action reconciliation.
func NewSendMessageTool(send AgentSendFunc, lead *domain.LeadRecord, actionKey func(context.Context, string) (string, error), options SendMessageOptions) (agentruntime.Tool, error) {
	if send == nil || lead == nil || actionKey == nil || strings.TrimSpace(options.CheckoutOrigin) == "" {
		return nil, errors.New("send_message requires sender, lead, action identity and checkout origin")
	}
	for _, id := range []string{lead.ID, lead.SellerID, lead.Platform, lead.ThreadID, lead.PostID, lead.BuyerID} {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("send_message requires trusted lead and marketplace scope")
		}
	}
	if options.Channel == "" {
		options.Channel = contracts.AgentPublicReply
	}
	if options.Channel != contracts.AgentPublicReply && options.Channel != contracts.AgentDirectMessage {
		return nil, errors.New("unsupported agent delivery channel")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	tool := &sendMessageTool{send: send, lead: lead, scope: *lead, actionKey: actionKey, options: options, startedActive: lead.State == domain.LeadActive}
	if lead.Checkout != nil {
		tool.initialCheckoutID = lead.Checkout.ID
	}
	return tool, nil
}

type sendMessageTool struct {
	send              AgentSendFunc
	lead              *domain.LeadRecord
	scope             domain.LeadRecord
	actionKey         func(context.Context, string) (string, error)
	options           SendMessageOptions
	initialCheckoutID string
	startedActive     bool
	pending           bool
	delivered         bool
}

func (*sendMessageTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "send_message",
		Description: "Send one sender-approved message template. Use {price} for the stored offer and {checkout_link} for checkout created this turn. Unknown prose may be rejected by the final sender.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
	}
}

func (t *sendMessageTool) Run(ctx context.Context, raw json.RawMessage) (agentruntime.ToolOutcome, error) {
	if err := ctx.Err(); err != nil {
		return agentruntime.ToolOutcome{}, err
	}
	if t.pending {
		return commerceResult(map[string]string{"status": "pending"}), ErrSendMessagePending
	}
	if t.delivered {
		return commerceBlocked(contracts.InvalidState), nil
	}
	text, ok := parseSendText(raw)
	if !ok {
		return commerceBlocked(contracts.InvalidArgument), nil
	}
	lead := t.lead
	if lead.ID != t.scope.ID || lead.SellerID != t.scope.SellerID || lead.Platform != t.scope.Platform || lead.ThreadID != t.scope.ThreadID || lead.PostID != t.scope.PostID || lead.BuyerID != t.scope.BuyerID {
		return commerceBlocked(contracts.InvalidState), nil
	}
	checkoutID := ""
	needsCheckout := strings.Contains(text, "{checkout_link}")
	switch lead.State {
	case domain.LeadActive:
		if needsCheckout {
			return commerceBlocked(contracts.InvalidState), nil
		}
	case domain.LeadReserved:
		if !needsCheckout || !t.startedActive || lead.Checkout == nil || lead.Checkout.ID == "" || lead.Checkout.ID == t.initialCheckoutID {
			return commerceBlocked(contracts.InvalidState), nil
		}
		checkoutID = lead.Checkout.ID
	default:
		return commerceBlocked(contracts.InvalidState), nil
	}
	// Preflight trusted terms and origin without passing a rendered bearer URL
	// into model-facing text. P4 expects the original approved template and
	// repeats current-fact checks and authoritative rendering before publishing.
	if _, err := RenderMessage(text, *lead, t.options.CheckoutOrigin, t.options.Now()); err != nil {
		return commerceBlocked(contracts.InvalidArgument), nil
	}
	key, err := t.actionKey(ctx, "send_message")
	if err != nil {
		if ctx.Err() != nil {
			return agentruntime.ToolOutcome{}, ctx.Err()
		}
		return agentruntime.ToolOutcome{}, errors.New("send_message action identity is unavailable")
	}
	if strings.TrimSpace(key) == "" {
		return agentruntime.ToolOutcome{}, errors.New("send_message action identity is required")
	}
	request := contracts.AgentMessageRequest{
		ActionID: key, LeadID: lead.ID, SellerID: lead.SellerID,
		Thread:     domain.ThreadRef{Platform: lead.Platform, ThreadID: lead.ThreadID},
		CheckoutID: checkoutID, Channel: t.options.Channel, Text: text,
	}
	result, err := t.send(ctx, request)
	if result.Status == contracts.AgentDeliveryPending {
		t.pending = true
		// The outer action recorder can observe pending before the error stops
		// model execution. This is not a success or a confirmed delivery.
		return commerceResult(map[string]string{"status": "pending"}), ErrSendMessagePending
	}
	// A rejection accompanied by publication evidence is contradictory. Stop
	// rather than invite a corrected model send that could publish twice.
	if result.Status == contracts.AgentDeliveryRejected && (result.Receipt.ActionID != "" || result.Receipt.MessageID != "" || !result.Receipt.PublishedAt.IsZero()) {
		return agentruntime.ToolOutcome{}, ErrSendMessageFailed
	}
	if err != nil {
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
		if result.Status == contracts.AgentDeliveryRejected && errors.As(err, &failure) && sendRejectionCode(failure.Code) {
			return commerceBlocked(failure.Code), nil
		}
		return agentruntime.ToolOutcome{}, ErrSendMessageFailed
	}
	receipt := result.Receipt
	if result.Status != contracts.AgentDeliveryDelivered || receipt.ActionID != key || strings.TrimSpace(receipt.MessageID) == "" || receipt.PublishedAt.IsZero() {
		return agentruntime.ToolOutcome{}, ErrSendMessageFailed
	}
	t.delivered = true
	out := commerceResult(map[string]string{"status": "delivered"})
	out.Stop = true
	return out, nil
}

func parseSendText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
		return "", false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 {
		return "", false
	}
	var text *string
	if json.Unmarshal(fields["text"], &text) != nil || text == nil || strings.TrimSpace(*text) == "" {
		return "", false
	}
	return *text, true
}

func sendRejectionCode(code contracts.Code) bool {
	switch code {
	case contracts.NotFound, contracts.InvalidArgument, contracts.InvalidState, contracts.PriceBelowMinimum, contracts.OutOfStock, contracts.OfferSuperseded, contracts.CheckoutExpired, contracts.VersionConflict, contracts.IdempotencyConflict, contracts.AgentChannelUnsupported, contracts.AgentMessageCapReached, contracts.AgentRateLimited:
		return true
	}
	return false
}
