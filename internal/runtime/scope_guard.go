package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
)

// ScopeDecision is P1-local control flow, not an agreed P2 output schema.
type ScopeDecision string

const (
	ScopeInScope  ScopeDecision = "in_scope"
	ScopeOffTopic ScopeDecision = "off_topic"
	ScopeMixed    ScopeDecision = "mixed"
)

// ScopeClassifier classifies the buyer snapshot without commerce tools. A live
// GenerateJSON adapter must use P2's agreed prompt/schema, not a keyword ban.
// Buyer history is untrusted data, not instructions to change the agent's role.
type ScopeClassifier func(context.Context, TurnInput) (ScopeDecision, error)

// ScopeTurnFunc accepts TurnRunner.RunTurn without changing its contract.
type ScopeTurnFunc func(context.Context, TurnInput) (LoopResult, error)

// ApprovedScopeRedirect must use an owner-approved catalog and the existing
// guarded sender. Bind it only after P2/P4 agree that path. It receives trusted
// scope/action identity, never model-authored redirect text. Nil blocks delivery.
type ApprovedScopeRedirect func(context.Context, Event, string) (contracts.AgentDeliveryResult, error)

type ScopeResult struct {
	Decision       ScopeDecision
	Turn           LoopResult `json:"-"`
	RedirectStatus contracts.AgentDeliveryStatus
}

var (
	ErrScopeUnavailable      = errors.New("buyer scope classifier is unavailable")
	ErrScopeUnknown          = errors.New("buyer request scope could not be established")
	ErrScopeBlocked          = errors.New("unrelated buyer work is blocked")
	ErrScopeMixedUnsupported = errors.New("mixed request requires an approved commerce-only projection")
	ErrScopeRedirectPending  = errors.New("approved scope redirect publication is pending")
	ErrScopeRedirectFailed   = errors.New("approved scope redirect delivery could not be confirmed")
)

// ScopeGuard gates one turn before negotiation/model/tool execution. Composition
// must actually use this wrapper; its existence does not protect other direct
// TurnRunner callers. Classification is a control layer, not semantic proof.
type ScopeGuard struct {
	classify ScopeClassifier
	run      ScopeTurnFunc
	redirect ApprovedScopeRedirect
}

// NewScopeGuard permits a nil classifier while owner assets are pending, but
// every request then fails closed. It never substitutes an unrestricted turn.
func NewScopeGuard(classify ScopeClassifier, run ScopeTurnFunc, redirect ApprovedScopeRedirect) (*ScopeGuard, error) {
	if run == nil {
		return nil, errors.New("scope guard requires a turn runner")
	}
	return &ScopeGuard{classify: classify, run: run, redirect: redirect}, nil
}

// RunTurn requires an ordered snapshot ending in the current buyer request.
// For initial outreach, the caller must include the original buyer post; an
// absent snapshot cannot silently bypass classification. Callers bound ctx and
// injected dependencies must honor it. This component does not change states,
// close/escalate leads, execute unrelated work or directly publish model text.
func (g *ScopeGuard) RunTurn(ctx context.Context, input TurnInput) (ScopeResult, error) {
	var result ScopeResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if g.classify == nil {
		return result, ErrScopeUnavailable
	}
	if strings.TrimSpace(input.Event.ID) == "" || strings.TrimSpace(input.Event.LeadID) == "" ||
		(input.Event.Trigger != TriggerLeadDiscovered && input.Event.Trigger != TriggerBuyerReply) || len(input.History) == 0 {
		return result, ErrScopeUnknown
	}
	for _, message := range input.History {
		if message.Role != "user" && message.Role != "assistant" {
			return result, ErrScopeUnknown
		}
	}
	last := input.History[len(input.History)-1]
	if last.Role != "user" || strings.TrimSpace(last.Content) == "" {
		return result, ErrScopeUnknown
	}
	input.History = slices.Clone(input.History)
	classifyInput := input
	classifyInput.History = slices.Clone(input.History)
	decision, err := g.classify(ctx, classifyInput)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return result, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return result, context.DeadlineExceeded
		}
		return result, ErrScopeUnavailable
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	switch decision {
	case ScopeInScope:
		result.Decision = ScopeInScope
		result.Turn, err = g.run(ctx, input)
		return result, err
	case ScopeMixed:
		result.Decision = ScopeMixed
		// There is no agreed commerce-only projection yet. Passing the original
		// mixed text or a model-claimed safe rewrite would fail open.
		return result, ErrScopeMixedUnsupported
	case ScopeOffTopic:
		result.Decision = ScopeOffTopic
	default:
		return result, ErrScopeUnknown
	}
	if g.redirect == nil {
		return result, ErrScopeBlocked
	}
	// Separate namespace from normal [eventID, modelCallID] action identities.
	encoded, _ := json.Marshal([3]string{"scope_redirect", input.Event.ID, input.Event.LeadID})
	actionID := string(encoded)
	delivery, err := g.redirect(ctx, input.Event, actionID)
	if delivery.Status == contracts.AgentDeliveryPending {
		result.RedirectStatus = contracts.AgentDeliveryPending
		return result, ErrScopeRedirectPending
	}
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return result, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return result, context.DeadlineExceeded
		}
		return result, ErrScopeRedirectFailed
	}
	receipt := delivery.Receipt
	if delivery.Status == contracts.AgentDeliveryDelivered && receipt.ActionID == actionID && strings.TrimSpace(receipt.MessageID) != "" && !receipt.PublishedAt.IsZero() {
		result.RedirectStatus = contracts.AgentDeliveryDelivered
		return result, nil
	}
	if delivery.Status == contracts.AgentDeliveryRejected && receipt.ActionID == "" && receipt.MessageID == "" && receipt.PublishedAt.IsZero() {
		result.RedirectStatus = contracts.AgentDeliveryRejected
		return result, ErrScopeBlocked
	}
	return result, ErrScopeRedirectFailed
}
