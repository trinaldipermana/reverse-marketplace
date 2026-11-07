package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
)

// ApplyFixedPriceToolPolicy installs the opt-in #54 mutation boundary while
// retaining the six existing tool definitions. The caller owns the feature
// selector; disabled mode returns the original tools without changing behavior.
//
// Enabled mode delegates only item reads and guarded message delivery. Offer,
// checkout and lead-state mutations are rejected before invoking their tools.
// This does not provide the trusted listing, approved templates or delivery
// binding required for the complete partner-link journey.
func ApplyFixedPriceToolPolicy(tools []Tool, enabled bool) ([]Tool, error) {
	if !enabled {
		return tools, nil
	}
	guarded := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			return nil, errors.New("fixed-price policy requires non-nil tools")
		}
		definition := tool.Definition()
		switch definition.Name {
		case "get_item", "send_message", "make_offer", "create_checkout", "escalate_to_seller", "close_lead":
		default:
			return nil, errors.New("fixed-price policy requires reviewed conversation tools")
		}
		guarded = append(guarded, &fixedPriceTool{tool: tool, definition: definition})
	}
	return guarded, nil
}

type fixedPriceTool struct {
	tool       Tool
	definition llm.ToolDefinition
}

func (t *fixedPriceTool) Definition() llm.ToolDefinition { return t.definition }

func (t *fixedPriceTool) Run(ctx context.Context, args json.RawMessage) (ToolOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ToolOutcome{}, err
	}
	switch t.definition.Name {
	case "get_item", "send_message":
		return t.tool.Run(ctx, args)
	default:
		// This existing rejection is recognized by the safe action recorder.
		return ToolOutcome{Output: `{"error":"INVALID_STATE"}`}, nil
	}
}
