package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
)

// TurnInput contains caller-supplied P2 instructions and an already ordered,
// lead-scoped history snapshot. It does not define a P4 retrieval contract.
type TurnInput struct {
	Event              Event
	Instructions       string
	TriggerInstruction string
	History            []llm.ChatMessage
}

// TurnMetadata binds trusted event identity and action-key lookup to one turn.
// ActionKey is available only inside the corresponding tool's Run context. The
// same event/call returns the same key; different same-name calls get distinct
// keys. Regenerating model calls is not durable operation replay.
type TurnMetadata struct {
	Event     Event
	ActionKey func(context.Context, string) (string, error)
}

// TurnToolFactory builds fresh wrappers around one mutable lead context. It
// must not perform business actions during construction or retain the context
// for another turn. Injecting it avoids a runtime -> runtime/tools import cycle.
type TurnToolFactory func(context.Context, *LeadContext, TurnMetadata) ([]Tool, error)

type TurnRunner struct {
	commerce ContextCommerce
	model    TurnModel
	factory  TurnToolFactory
	limits   LoopLimits
}

func NewTurnRunner(commerce ContextCommerce, model TurnModel, factory TurnToolFactory, limits LoopLimits) (*TurnRunner, error) {
	if commerce == nil || factory == nil {
		return nil, errors.New("turn runner requires commerce and a tool factory")
	}
	validated, err := NewToolLoop(model, nil, limits)
	if err != nil {
		return nil, err
	}
	return &TurnRunner{commerce: commerce, model: model, factory: factory, limits: validated.limits}, nil
}

// RunTurn composes context loading, safe request construction and bounded tool
// execution. The caller supplies history/prompts and serializes lead events
// (for example through Worker). This method does not deduplicate events, persist
// actions, fetch P4 history, deliver model text, or retry partial turns.
func (r *TurnRunner) RunTurn(ctx context.Context, input TurnInput) (LoopResult, error) {
	if strings.TrimSpace(input.Event.ID) == "" || strings.TrimSpace(input.Event.LeadID) == "" {
		return LoopResult{}, errors.New("turn requires event and lead IDs")
	}
	if input.Event.Trigger != TriggerLeadDiscovered && input.Event.Trigger != TriggerBuyerReply {
		return LoopResult{}, errors.New("unsupported turn trigger")
	}
	// The same budget covers dependency loading and factory construction as well
	// as the model loop. Nested request/loop deadlines cannot extend it.
	ctx, cancel := context.WithTimeout(ctx, r.limits.Timeout)
	defer cancel()
	loaded, err := LoadLeadContext(ctx, r.commerce, input.Event.LeadID)
	if err != nil {
		return LoopResult{}, err
	}
	request, err := loaded.BuildRequest(input.Instructions, input.TriggerInstruction, input.History)
	if err != nil {
		return LoopResult{}, err
	}
	metadata := TurnMetadata{Event: input.Event}
	metadata.ActionKey = func(ctx context.Context, name string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		identity, ok := ctx.Value(turnIdentityKey{}).(turnIdentity)
		if !ok || identity.eventID != input.Event.ID || identity.name != name {
			return "", errors.New("action identity is unavailable outside the matching tool call")
		}
		// JSON encoding separates arbitrary IDs without delimiter collisions.
		key, _ := json.Marshal([2]string{identity.eventID, identity.callID})
		return string(key), nil
	}
	tools, err := r.factory(ctx, &loaded, metadata)
	if err != nil {
		return LoopResult{}, err
	}
	model := &turnCallModel{model: r.model}
	bound := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			return LoopResult{}, errors.New("turn factory returned a nil tool")
		}
		bound = append(bound, &turnBoundTool{tool: tool, definition: tool.Definition(), eventID: input.Event.ID, model: model})
	}
	loop, err := NewToolLoop(model, bound, r.limits)
	if err != nil {
		return LoopResult{}, err
	}
	return loop.Run(ctx, request)
}

// The adapter retains only this response's call metadata. ToolLoop still owns
// protocol validation, caps and ordering before any wrapper is executed.
type turnCallModel struct {
	model   TurnModel
	pending []llm.ToolCall
}

func (m *turnCallModel) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	m.pending = nil
	response, err := m.model.Generate(ctx, request)
	if err == nil {
		m.pending = append([]llm.ToolCall(nil), response.ToolCalls...)
	}
	return response, err
}

type turnIdentityKey struct{}
type turnIdentity struct{ eventID, callID, name string }

type turnBoundTool struct {
	tool       Tool
	definition llm.ToolDefinition
	eventID    string
	model      *turnCallModel
}

func (t *turnBoundTool) Definition() llm.ToolDefinition { return t.definition }

func (t *turnBoundTool) Run(ctx context.Context, args json.RawMessage) (ToolOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ToolOutcome{}, err
	}
	for i, call := range t.model.pending {
		if call.Name == t.definition.Name && bytes.Equal(call.Args, args) {
			t.model.pending = append(t.model.pending[:i], t.model.pending[i+1:]...)
			identity := turnIdentity{eventID: t.eventID, callID: call.ID, name: call.Name}
			return t.tool.Run(context.WithValue(ctx, turnIdentityKey{}, identity), args)
		}
	}
	return ToolOutcome{}, errors.New("tool call identity was not provided by the current model response")
}
