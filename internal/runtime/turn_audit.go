package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/runtime/actionlog"
)

var ErrTurnAudit = errors.New("turn action-log write failed")

// Causes remain available to internal errors.Is/As, not in buyer-facing text.
type turnAuditFailure struct{ cause error }

func (e turnAuditFailure) Error() string        { return ErrTurnAudit.Error() }
func (e turnAuditFailure) Unwrap() error        { return e.cause }
func (e turnAuditFailure) Is(target error) bool { return target == ErrTurnAudit }

type turnAudit struct {
	recorder  TurnRecorder
	event     Event
	sellerID  string
	started   bool
	failure   error
	toolError error
}

func (a *turnAudit) record(ctx context.Context, entry actionlog.Entry) error {
	if a.recorder == nil {
		return nil
	}
	entry.EventID, entry.LeadID, entry.SellerID = a.event.ID, a.event.LeadID, a.sellerID
	if err := a.recorder.Record(ctx, entry); err != nil {
		a.failure = err
		return turnAuditFailure{cause: err}
	}
	return nil
}

func (a *turnAudit) finish(ctx context.Context, turnErr *error) {
	if a.recorder == nil {
		return
	}
	if a.failure != nil {
		*turnErr = turnAuditFailure{cause: errors.Join(a.failure, *turnErr)}
		return
	}
	entry := actionlog.Entry{Kind: actionlog.KindTurnCompleted, Status: actionlog.StatusOK, Code: actionlog.CodeOK}
	if *turnErr != nil {
		entry.Status, entry.Code = actionlog.StatusFailed, auditErrorCode(*turnErr)
		if !a.started {
			entry.Kind = actionlog.KindError
		}
	} else if !a.started {
		return
	}
	if err := a.record(ctx, entry); err != nil {
		*turnErr = turnAuditFailure{cause: errors.Join(err, *turnErr)}
	}
}

// Only allowlisted status/error codes are extracted. Output text, arguments,
// model prose and service error messages are never forwarded to the recorder.
func (a *turnAudit) tool(ctx context.Context, name, callID string, outcome ToolOutcome, toolErr error) error {
	if a.recorder == nil {
		return nil
	}
	entry := actionlog.Entry{Kind: actionlog.KindTool, Status: actionlog.StatusOK, Code: actionlog.CodeOK, ToolCallID: callID}
	switch actionlog.ToolName(name) {
	case actionlog.ToolGetItem, actionlog.ToolMakeOffer, actionlog.ToolCreateCheckout, actionlog.ToolEscalate, actionlog.ToolClose, actionlog.ToolSendMessage:
		entry.Tool = actionlog.ToolName(name)
	default:
		entry.Kind, entry.Status, entry.Code = actionlog.KindError, actionlog.StatusFailed, actionlog.CodeInvalidArgument
		return a.record(ctx, entry)
	}
	var classified struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal([]byte(outcome.Output), &classified)
	if entry.Tool == actionlog.ToolSendMessage {
		switch classified.Status {
		case string(contracts.AgentDeliveryPending):
			entry.Code = actionlog.CodePending
			return a.record(ctx, entry)
		case string(contracts.AgentDeliveryDelivered):
			return a.record(ctx, entry)
		}
	}
	if toolErr != nil {
		entry.Status, entry.Code = actionlog.StatusFailed, auditErrorCode(toolErr)
	} else if classified.Error != "" {
		entry.Kind, entry.Status, entry.Code = actionlog.KindGuardrail, actionlog.StatusBlocked, auditBusinessCode(classified.Error)
	} else if entry.Tool == actionlog.ToolSendMessage {
		// Stop alone, or an unrecognized result, is not proof of delivery.
		entry.Kind, entry.Status, entry.Code = actionlog.KindError, actionlog.StatusFailed, actionlog.CodeInvalidArgument
	}
	return a.record(ctx, entry)
}

func auditErrorCode(err error) actionlog.ResultCode {
	switch {
	case errors.Is(err, context.Canceled):
		return actionlog.CodeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return actionlog.CodeTimedOut
	case errors.Is(err, ErrRoundLimit), errors.Is(err, ErrToolCallLimit):
		return actionlog.CodeLimitReached
	case errors.Is(err, ErrLeadNotActive), errors.Is(err, ErrContextScope):
		return actionlog.CodeInvalidState
	}
	var service *contracts.Error
	if errors.As(err, &service) {
		return auditBusinessCode(string(service.Code))
	}
	return actionlog.CodeDependencyUnavailable
}

func auditBusinessCode(code string) actionlog.ResultCode {
	switch contracts.Code(code) {
	case contracts.NotFound:
		return actionlog.CodeNotFound
	case contracts.InvalidArgument:
		return actionlog.CodeInvalidArgument
	case contracts.InvalidState:
		return actionlog.CodeInvalidState
	case contracts.PriceBelowMinimum:
		return actionlog.CodePriceBelowMinimum
	case contracts.OutOfStock:
		return actionlog.CodeOutOfStock
	case contracts.OfferSuperseded:
		return actionlog.CodeOfferSuperseded
	case contracts.CheckoutExpired:
		return actionlog.CodeCheckoutExpired
	case contracts.VersionConflict:
		return actionlog.CodeVersionConflict
	case contracts.IdempotencyConflict:
		return actionlog.CodeIdempotencyConflict
	case contracts.DependencyUnavailable:
		return actionlog.CodeDependencyUnavailable
	case contracts.AgentMessageCapReached, contracts.AgentRateLimited:
		return actionlog.CodeLimitReached
	default:
		return actionlog.CodeInvalidArgument
	}
}
