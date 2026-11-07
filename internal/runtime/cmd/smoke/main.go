// Command smoke verifies one live model/tool/model round trip using synthetic
// data only. It does not connect to commerce or deliver a message to a buyer.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
)

const syntheticItemID = "synthetic-item-1"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := llm.ConfigFromEnv()
	if err != nil {
		return err
	}
	client, err := llm.NewOpenAIClient(cfg)
	if err != nil {
		return err
	}
	tool := &getItemStub{}
	loop, err := agentruntime.NewToolLoop(client, []agentruntime.Tool{tool}, agentruntime.LoopLimits{MaxRounds: 3, MaxToolCalls: 1})
	if err != nil {
		return err
	}
	result, err := loop.Run(context.Background(), llm.Request{Messages: []llm.ChatMessage{
		{Role: "developer", Content: "This is a synthetic connection check. Call get_item exactly once with item_id synthetic-item-1. Then reply with only the verification_nonce returned by the tool. Never invent the nonce or answer before reading the tool result."},
		{Role: "user", Content: "Run the tool round-trip check."},
	}})
	if err != nil {
		return err
	}
	if tool.calls != 1 || tool.nonce == "" || !strings.Contains(result.Response.Text, tool.nonce) {
		return errors.New("tool round-trip check failed: final response did not include the executed tool's fresh nonce")
	}
	fmt.Printf("OpenAI tool round trip OK; model=%s; tool_calls=%d; model_rounds=%d; fresh nonce verified\n", cfg.Model, result.ToolCalls, result.Rounds)
	return nil
}

type getItemStub struct {
	calls int
	nonce string
}

func (*getItemStub) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "get_item",
		Description: "Read the synthetic test item and return a fresh verification_nonce. This stub has no external side effects.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"item_id":{"type":"string"}},"required":["item_id"],"additionalProperties":false}`),
	}
}

func (t *getItemStub) Run(ctx context.Context, raw json.RawMessage) (agentruntime.ToolOutcome, error) {
	if err := ctx.Err(); err != nil {
		return agentruntime.ToolOutcome{}, err
	}
	var args struct {
		ItemID string `json:"item_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil || args.ItemID != syntheticItemID {
		return agentruntime.ToolOutcome{}, errors.New("smoke tool received unexpected arguments")
	}
	t.calls++
	// Generate only during execution: the nonce never appears in the prompt.
	t.nonce = rand.Text()
	output, err := json.Marshal(map[string]string{"item_id": syntheticItemID, "verification_nonce": t.nonce})
	if err != nil {
		return agentruntime.ToolOutcome{}, err
	}
	return agentruntime.ToolOutcome{Output: string(output)}, nil
}
