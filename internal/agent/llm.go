package agent

import (
	"context"
	"encoding/json"
)

// LLM is the model boundary used by the runtime. It never executes tools.
type LLM interface {
	Generate(context.Context, Request) (Response, error)
	GenerateJSON(context.Context, JSONRequest, any) error
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ToolDefinition struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type ToolResult struct {
	CallID string
	Output string
}

// Continuation is opaque model state. Keep it for subsequent calls in this turn;
// it includes reasoning items required by the Responses tool-calling protocol.
// Do not persist or log it as part of the buyer's conversation.
type Continuation struct{ input []json.RawMessage }

type Request struct {
	Messages     []ChatMessage
	Tools        []ToolDefinition
	Continuation *Continuation
	ToolResults  []ToolResult
}

type Response struct {
	Text         string
	ToolCalls    []ToolCall
	Continuation *Continuation
}

type JSONRequest struct {
	Instructions string
	Input        string
	SchemaName   string
	Schema       json.RawMessage
}
