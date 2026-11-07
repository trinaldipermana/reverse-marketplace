package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
)

// TurnModel is the part of the model client needed to execute a turn.
type TurnModel interface {
	Generate(context.Context, llm.Request) (llm.Response, error)
}

// Tool owns argument/schema validation and business guardrails before acting.
// Expected rejections are ToolOutcome output; infrastructure failures are errors.
// Bind trusted lead/seller context in the implementation, not in model arguments.
type Tool interface {
	Definition() llm.ToolDefinition
	Run(context.Context, json.RawMessage) (ToolOutcome, error)
}

type ToolOutcome struct {
	Output string
	// Stop ends the turn after a successful terminal action, such as sending a
	// message or escalating. No later tool in the same response is executed.
	Stop bool
}

type LoopLimits struct {
	MaxRounds    int
	MaxToolCalls int
	Timeout      time.Duration
}

type LoopResult struct {
	Response    llm.Response
	ToolResults []llm.ToolResult
	Rounds      int
	ToolCalls   int
	Stopped     bool
}

var (
	ErrRoundLimit    = errors.New("agent model-round limit reached")
	ErrToolCallLimit = errors.New("agent tool-call limit reached")
)

// ToolLoop executes a single bounded turn. It does not load leads, deliver model
// text, persist outcomes, retry actions, or provide replay/idempotency guarantees.
type ToolLoop struct {
	model       TurnModel
	tools       map[string]Tool
	definitions []llm.ToolDefinition
	limits      LoopLimits
}

func NewToolLoop(model TurnModel, tools []Tool, limits LoopLimits) (*ToolLoop, error) {
	if model == nil {
		return nil, errors.New("tool loop requires a model")
	}
	if limits.MaxRounds == 0 {
		limits.MaxRounds = 5
	}
	if limits.MaxToolCalls == 0 {
		limits.MaxToolCalls = 8
	}
	if limits.Timeout == 0 {
		limits.Timeout = 60 * time.Second
	}
	if limits.MaxRounds < 1 || limits.MaxToolCalls < 1 || limits.Timeout < 0 {
		return nil, errors.New("tool loop limits must be positive")
	}
	loop := &ToolLoop{model: model, tools: make(map[string]Tool, len(tools)), limits: limits}
	for _, tool := range tools {
		if tool == nil {
			return nil, errors.New("cannot register a nil tool")
		}
		definition := tool.Definition()
		if definition.Name == "" || !json.Valid(definition.Schema) {
			return nil, errors.New("tool requires a name and valid JSON schema")
		}
		if _, exists := loop.tools[definition.Name]; exists {
			return nil, errors.New("duplicate tool name")
		}
		definition.Schema = bytes.Clone(definition.Schema)
		loop.tools[definition.Name] = tool
		loop.definitions = append(loop.definitions, definition)
	}
	return loop, nil
}

// Run uses registered tool definitions rather than req.Tools. Preserve returned
// counters/results even on error: earlier tools may already have acted. Model
// text is returned for inspection only; it is never sent to a buyer here.
// Models and tools must honor context cancellation for deadlines to take effect.
func (l *ToolLoop) Run(ctx context.Context, req llm.Request) (LoopResult, error) {
	ctx, cancel := context.WithTimeout(ctx, l.limits.Timeout)
	defer cancel()
	var result LoopResult
	req.Tools = l.definitions
	for result.Rounds < l.limits.MaxRounds {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Rounds++
		response, err := l.model.Generate(ctx, req)
		if err != nil {
			return result, err
		}
		result.Response = response
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if len(response.ToolCalls) == 0 {
			return result, nil
		}
		// Reject malformed protocol metadata before any tool can have a side
		// effect, including a terminal tool that would otherwise stop early.
		if response.Continuation == nil {
			return result, errors.New("model returned tool calls without continuation")
		}
		callIDs := make(map[string]bool, len(response.ToolCalls))
		for _, call := range response.ToolCalls {
			if call.ID == "" || callIDs[call.ID] {
				return result, errors.New("model tool calls require unique non-empty IDs")
			}
			callIDs[call.ID] = true
		}
		var outputs []llm.ToolResult
		for _, call := range response.ToolCalls {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if result.ToolCalls == l.limits.MaxToolCalls {
				return result, ErrToolCallLimit
			}
			result.ToolCalls++
			var outcome ToolOutcome
			tool, exists := l.tools[call.Name]
			switch {
			case !exists:
				outcome.Output = `{"error":"unknown_tool"}`
			case !objectArguments(call.Args):
				outcome.Output = `{"error":"invalid_arguments"}`
			default:
				outcome, err = tool.Run(ctx, call.Args)
				if err != nil {
					return result, err
				}
			}
			output := llm.ToolResult{CallID: call.ID, Output: outcome.Output}
			outputs = append(outputs, output)
			result.ToolResults = append(result.ToolResults, output)
			if outcome.Stop {
				result.Stopped = true
				return result, nil
			}
		}
		req = llm.Request{Continuation: response.Continuation, ToolResults: outputs, Tools: l.definitions}
	}
	return result, ErrRoundLimit
}

// This only checks JSON object syntax. Registered wrappers validate their full
// argument schema and business policy before performing any side effect.
func objectArguments(args json.RawMessage) bool {
	trimmed := bytes.TrimSpace(args)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}
