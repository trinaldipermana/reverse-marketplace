package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
)

// NewJSONScopeClassifier binds the existing GenerateJSON client to ScopeGuard.
// P2 supplies approved instructions, schema and a decoder for that schema; this
// adapter supplies no default classifier prompt or assumed P2 result fields.
// The template's Input is replaced with the ordered untrusted history snapshot.
// No negotiation instructions, private seller policy or tools are sent.
func NewJSONScopeClassifier(generator interface {
	GenerateJSON(context.Context, llm.JSONRequest, any) error
}, template llm.JSONRequest, decode func(json.RawMessage) (ScopeDecision, error)) (ScopeClassifier, error) {
	if generator == nil || decode == nil || strings.TrimSpace(template.Instructions) == "" || strings.TrimSpace(template.SchemaName) == "" || !json.Valid(template.Schema) {
		return nil, errors.New("scope classifier requires a generator and approved prompt/schema decoder")
	}
	template.Schema = bytes.Clone(template.Schema)
	return func(ctx context.Context, input TurnInput) (ScopeDecision, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if len(input.History) == 0 {
			return "", ErrScopeUnknown
		}
		payload, err := json.Marshal(struct {
			History []llm.ChatMessage `json:"history"`
		}{History: input.History})
		if err != nil {
			return "", ErrScopeUnknown
		}
		request := template
		request.Schema = bytes.Clone(template.Schema)
		request.Input = string(payload)
		var output json.RawMessage
		err = generator.GenerateJSON(ctx, request, &output)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return "", context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", context.DeadlineExceeded
		}
		if err != nil {
			return "", ErrScopeUnavailable
		}
		if !json.Valid(output) {
			return "", ErrScopeUnknown
		}
		decision, err := decode(output)
		if err != nil {
			return "", ErrScopeUnknown
		}
		switch decision {
		case ScopeInScope, ScopeOffTopic, ScopeMixed:
			return decision, nil
		default:
			return "", ErrScopeUnknown
		}
	}, nil
}
