package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	agenttools "github.com/trinaldirizki/reverse-marketplace/internal/runtime/tools"
)

type promptScopeConfig struct {
	Instructions  string          `json:"instructions"`
	SchemaName    string          `json:"schema_name"`
	Schema        json.RawMessage `json:"schema"`
	DecisionField string          `json:"decision_field"`
}

func readPromptScope(t *testing.T) promptScopeConfig {
	t.Helper()
	raw, err := os.ReadFile("../../prompts/buyer_turn_scope.json")
	if err != nil {
		t.Fatal(err)
	}
	var config promptScopeConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func (c promptScopeConfig) template() llm.JSONRequest {
	return llm.JSONRequest{Instructions: c.Instructions, SchemaName: c.SchemaName, Schema: c.Schema}
}

// This mirrors the existing P4 #48 decision_field binding, not a new output API.
func (c promptScopeConfig) decode(raw json.RawMessage) (runtime.ScopeDecision, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	var decision runtime.ScopeDecision
	err := json.Unmarshal(fields[c.DecisionField], &decision)
	return decision, err
}

type promptGenerator func(context.Context, llm.JSONRequest, any) error

func (g promptGenerator) GenerateJSON(ctx context.Context, request llm.JSONRequest, output any) error {
	return g(ctx, request, output)
}

func TestPromptAssetsBindScopeGuard(t *testing.T) {
	config := readPromptScope(t)
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if json.Unmarshal(config.Schema, &schema) != nil || schema.Type != "object" || schema.AdditionalProperties || !slices.Equal(schema.Required, []string{config.DecisionField}) {
		t.Fatal("scope asset must declare a strict root object with the decision field required")
	}
	field := schema.Properties[config.DecisionField]
	if field.Type != "string" || !slices.Equal(field.Enum, []string{"in_scope", "off_topic", "mixed"}) {
		t.Fatal("scope schema does not match the runtime's accepted decisions")
	}
	instructions, err := os.ReadFile("../../prompts/seller_negotiation.txt")
	if err != nil || strings.TrimSpace(string(instructions)) == "" {
		t.Fatal("negotiation instructions unavailable", err)
	}
	for _, test := range []struct {
		output string
		want   error
		runs   int
	}{
		{`{"scope":"in_scope"}`, nil, 1},
		{`{"scope":"off_topic"}`, runtime.ErrScopeBlocked, 0},
		{`{"scope":"mixed"}`, runtime.ErrScopeMixedUnsupported, 0},
		{`{"scope":"unknown"}`, runtime.ErrScopeUnavailable, 0},
		{`{}`, runtime.ErrScopeUnavailable, 0},
	} {
		t.Run(test.output, func(t *testing.T) {
			generator := promptGenerator(func(_ context.Context, request llm.JSONRequest, output any) error {
				if strings.Contains(request.Input, "PRIVATE") {
					t.Fatal("private negotiation data reached classifier")
				}
				*output.(*json.RawMessage) = json.RawMessage(test.output)
				return nil
			})
			classify, err := runtime.NewJSONScopeClassifier(generator, config.template(), config.decode)
			if err != nil {
				t.Fatal(err)
			}
			runs := 0
			guard, err := runtime.NewScopeGuard(classify, func(context.Context, runtime.TurnInput) (runtime.LoopResult, error) {
				runs++
				return runtime.LoopResult{}, nil
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = guard.RunTurn(t.Context(), runtime.TurnInput{Event: runtime.Event{ID: "prompt-event", LeadID: "prompt-lead", Trigger: runtime.TriggerBuyerReply}, Instructions: "PRIVATE", History: []llm.ChatMessage{{Role: "user", Content: "What is the price?"}}})
			if !errors.Is(err, test.want) || runs != test.runs {
				t.Fatalf("runs=%d error=%v", runs, err)
			}
		})
	}
}

func livePromptClient(t *testing.T) *llm.OpenAIClient {
	t.Helper()
	if os.Getenv("RUN_PROMPT_EVAL") != "1" {
		t.Skip("opt-in live synthetic prompt eval; set RUN_PROMPT_EVAL=1")
	}
	config, err := llm.ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	client, err := llm.NewOpenAIClient(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestPromptLiveScope(t *testing.T) {
	client := livePromptClient(t)
	config := readPromptScope(t)
	classify, err := runtime.NewJSONScopeClassifier(client, config.template(), config.decode)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID      string                `json:"id"`
		History []llm.ChatMessage     `json:"history"`
		Want    runtime.ScopeDecision `json:"scope"`
	}
	raw, err := os.ReadFile("../../evals/buyer_turn_scope.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatal("scope eval fixture unavailable", err)
	}
	for _, test := range cases {
		t.Run(test.ID, func(t *testing.T) {
			got, err := classify(t.Context(), runtime.TurnInput{History: test.History})
			if err != nil || got != test.Want {
				t.Fatalf("expected=%s actual=%s error=%v", test.Want, got, err)
			}
			t.Logf("expected=%s actual=%s", test.Want, got)
		})
	}
}

// All commerce and delivery effects stay inside temporary local fixtures.
// This checks live tool selection, not the shared app/UI or P4 prompt-history loader.
func TestPromptLivePurchase(t *testing.T) {
	client := livePromptClient(t)
	f := newPurchaseFixture(t)
	text, err := os.ReadFile("../../prompts/seller_negotiation.txt")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := marketplace.NewAgentSender(f.commerce, f.conversation.Threads, f.conversation.Threads, purchaseOrigin, f.now)
	if err != nil {
		t.Fatal(err)
	}
	factory := func(_ context.Context, loaded *runtime.LeadContext, metadata runtime.TurnMetadata) ([]runtime.Tool, error) {
		tools, err := agenttools.NewCommerceTools(f.commerce, &loaded.Lead, metadata.ActionKey)
		if err != nil {
			return nil, err
		}
		send, err := agenttools.NewSendMessageTool(sender.SendAgentMessage, &loaded.Lead, metadata.ActionKey, agenttools.SendMessageOptions{CheckoutOrigin: purchaseOrigin, Now: f.now})
		return append(tools, send), err
	}
	runner, err := runtime.NewTurnRunner(f.commerce, client, factory, runtime.LoopLimits{Timeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	config := readPromptScope(t)
	classify, err := runtime.NewJSONScopeClassifier(client, config.template(), config.decode)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := runtime.NewScopeGuard(classify, runner.RunTurn, nil)
	if err != nil {
		t.Fatal(err)
	}
	history := []llm.ChatMessage{{Role: "user", Content: "I need a used laptop for coding with 16GB RAM, under Rp 6 million, in Jakarta."}}
	input := runtime.TurnInput{Event: runtime.Event{ID: "live-initial", LeadID: f.lead.ID, Trigger: runtime.TriggerLeadDiscovered}, Instructions: string(text), TriggerInstruction: "Process the initial seller outreach for this saved buyer request.", History: history}
	initial, err := guard.RunTurn(t.Context(), input)
	if err != nil || !initial.Turn.Stopped {
		t.Fatalf("initial turn stopped=%v error=%v", initial.Turn.Stopped, err)
	}
	offered := f.loadLead(t)
	if offered.Offer == nil || offered.Offer.PriceIDR <= 0 || offered.State != domain.LeadActive || offered.Checkout != nil || len(f.history(t)) != 1 {
		t.Fatal("initial turn did not publish exactly one stored offer without reserving")
	}
	f.advance()
	history = append(history, llm.ChatMessage{Role: "assistant", Content: f.history(t)[0].Text}, llm.ChatMessage{Role: "user", Content: "I accept that offer. Please send the checkout link."})
	input.Event = runtime.Event{ID: "live-accept", LeadID: f.lead.ID, Trigger: runtime.TriggerBuyerReply, MessageID: "live-buyer-reply"}
	input.TriggerInstruction = "Process the current saved buyer reply."
	input.History = history
	accepted, err := guard.RunTurn(t.Context(), input)
	if err != nil || !accepted.Turn.Stopped {
		t.Fatalf("acceptance stopped=%v error=%v", accepted.Turn.Stopped, err)
	}
	lead := f.loadLead(t)
	messages := f.history(t)
	if lead.State != domain.LeadReserved || lead.Checkout == nil || lead.Checkout.OfferID != offered.Offer.ID || len(messages) != 2 || !strings.Contains(messages[1].Text, lead.Checkout.URL) {
		t.Fatal("acceptance did not publish one checkout bound to the stored offer")
	}
	t.Log("live initial offer and accepted-offer checkout published through actual local tools and guarded sender")
}
