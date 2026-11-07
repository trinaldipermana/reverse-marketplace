// Package actionlog provides P1's internal safe audit metadata. It is not a P3
// HTTP schema, a durable event queue, or evidence that a buyer action occurred.
package actionlog

import "errors"

type Kind string
type Status string
type ToolName string
type ResultCode string

const (
	KindDiscovery             Kind       = "discovery"
	KindTool                  Kind       = "tool"
	KindGuardrail             Kind       = "guardrail"
	KindState                 Kind       = "state"
	KindError                 Kind       = "error"
	StatusOK                  Status     = "ok"
	StatusBlocked             Status     = "blocked"
	StatusFailed              Status     = "failed"
	ToolGetItem               ToolName   = "get_item"
	ToolMakeOffer             ToolName   = "make_offer"
	ToolSendMessage           ToolName   = "send_message"
	ToolCreateCheckout        ToolName   = "create_checkout"
	ToolEscalate              ToolName   = "escalate_to_seller"
	ToolClose                 ToolName   = "close_lead"
	CodeOK                    ResultCode = "OK"
	CodeNotFound              ResultCode = "NOT_FOUND"
	CodeInvalidArgument       ResultCode = "INVALID_ARGUMENT"
	CodeInvalidState          ResultCode = "INVALID_STATE"
	CodePriceBelowMinimum     ResultCode = "PRICE_BELOW_MINIMUM"
	CodeOutOfStock            ResultCode = "OUT_OF_STOCK"
	CodeOfferSuperseded       ResultCode = "OFFER_SUPERSEDED"
	CodeCheckoutExpired       ResultCode = "CHECKOUT_EXPIRED"
	CodeVersionConflict       ResultCode = "VERSION_CONFLICT"
	CodeIdempotencyConflict   ResultCode = "IDEMPOTENCY_CONFLICT"
	CodeDependencyUnavailable ResultCode = "DEPENDENCY_UNAVAILABLE"
	CodeCanceled              ResultCode = "CANCELED"
	CodeTimedOut              ResultCode = "TIMED_OUT"
	CodeLimitReached          ResultCode = "LIMIT_REACHED"
)

var ErrInvalidEntry = errors.New("invalid action-log entry")

var toolSummary = map[ToolName]string{
	ToolGetItem: "item retrieved", ToolMakeOffer: "offer recorded", ToolSendMessage: "message delivery succeeded",
	ToolCreateCheckout: "checkout created", ToolEscalate: "seller handoff requested", ToolClose: "lead closure requested",
}
var resultSummary = map[ResultCode]string{
	CodeOK: "action succeeded", CodeNotFound: "resource not found", CodeInvalidArgument: "invalid arguments", CodeInvalidState: "action not allowed in current state",
	CodePriceBelowMinimum: "offer below seller minimum", CodeOutOfStock: "item unavailable", CodeOfferSuperseded: "offer superseded", CodeCheckoutExpired: "checkout expired",
	CodeVersionConflict: "record changed", CodeIdempotencyConflict: "action identity conflict", CodeDependencyUnavailable: "dependency unavailable",
	CodeCanceled: "runtime work canceled", CodeTimedOut: "runtime deadline reached", CodeLimitReached: "runtime limit reached",
}

// Entry has no free-form summary, model content or raw error field. IDs must be
// actual event/lead/seller identifiers, never tokens or model-supplied prose.
// IDs are opaque: preserve their upstream representation without adding an
// alphabet or length restriction absent from the worker and commerce contracts.
// The recorder validates this value before forwarding it to a sink.
type Entry struct {
	EventID  string     `json:"event_id"`
	LeadID   string     `json:"lead_id,omitempty"`
	SellerID string     `json:"seller_id,omitempty"`
	Kind     Kind       `json:"kind"`
	Status   Status     `json:"status"`
	Tool     ToolName   `json:"tool,omitempty"`
	Code     ResultCode `json:"code"`
}

func (e Entry) Validate() error {
	if e.EventID == "" {
		return ErrInvalidEntry
	}
	for _, id := range []string{e.LeadID, e.SellerID} {
		if id == "" && e.Kind != KindError {
			return ErrInvalidEntry
		}
	}
	if _, ok := resultSummary[e.Code]; !ok {
		return ErrInvalidEntry
	}
	if e.Tool != "" {
		if _, ok := toolSummary[e.Tool]; !ok {
			return ErrInvalidEntry
		}
	}
	switch e.Kind {
	case KindDiscovery, KindState:
		if e.Status != StatusOK || e.Code != CodeOK || e.Tool != "" {
			return ErrInvalidEntry
		}
	case KindTool:
		if e.Tool == "" || (e.Status != StatusOK && e.Status != StatusFailed) {
			return ErrInvalidEntry
		}
	case KindGuardrail:
		if e.Tool == "" || e.Status != StatusBlocked {
			return ErrInvalidEntry
		}
	case KindError:
		if e.Status != StatusFailed {
			return ErrInvalidEntry
		}
	default:
		return ErrInvalidEntry
	}
	if (e.Status == StatusOK) != (e.Code == CodeOK) {
		return ErrInvalidEntry
	}
	return nil
}

// Summary is generated only from allowlisted metadata. Do not replace it with
// model reasoning or the message text of an underlying service error.
func (e Entry) Summary() string {
	if e.Validate() != nil {
		return "invalid action entry"
	}
	if e.Status != StatusOK {
		return resultSummary[e.Code]
	}
	switch e.Kind {
	case KindDiscovery:
		return "lead received for processing"
	case KindState:
		return "lead state changed"
	case KindTool:
		return toolSummary[e.Tool]
	}
	return resultSummary[e.Code]
}
