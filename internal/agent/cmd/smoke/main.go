// Command smoke checks the OpenAI connection with synthetic data only.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/agent"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := agent.ConfigFromEnv()
	if err != nil {
		return err
	}
	client, err := agent.NewOpenAIClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var result struct {
		OK bool `json:"ok"`
	}
	err = client.GenerateJSON(ctx, agent.JSONRequest{
		Instructions: "This is a connection test. Return ok set to true.",
		Input:        "Check the connection using synthetic data only.",
		SchemaName:   "connection_check",
		Schema:       json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
	}, &result)
	if err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("connection check returned ok=false")
	}
	fmt.Printf("OpenAI connection OK; model=%s; structured JSON decoded\n", cfg.Model)
	return nil
}
