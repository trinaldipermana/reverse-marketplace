// Command server runs the opt-in, loopback-only marketplace demo API.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/app"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	port := flag.Int("port", 8080, "loopback HTTP port")
	instructionsPath := flag.String("turn-instructions", "", "path to approved negotiation instructions")
	scopePath := flag.String("scope-config", "", "path to the approved scope classifier configuration")
	flag.Parse()
	if *port < 1 || *port > 65535 || *instructionsPath == "" || *scopePath == "" || os.Getenv("DEMO_RESET_TOKEN") == "" {
		return errors.New("provide a valid port, -turn-instructions, -scope-config, and DEMO_RESET_TOKEN")
	}
	instructions, err := os.ReadFile(*instructionsPath)
	if err != nil {
		return err
	}
	config, err := llm.ConfigFromEnv()
	if err != nil {
		return err
	}
	model, err := llm.NewOpenAIClient(config)
	if err != nil {
		return err
	}
	classifier, err := loadClassifier(model, *scopePath)
	if err != nil {
		return err
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	origin := "http://" + address
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	demo, err := app.NewDemoApp(ctx, app.DemoConfig{
		CheckoutOrigin: origin,
		ResetToken:     os.Getenv("DEMO_RESET_TOKEN"),
		Model:          model,
		Instructions:   string(instructions),
		Classify:       classifier,
	})
	if err != nil {
		return err
	}
	defer demo.Close()

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           demo,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      100 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Printf("Demo API listening on %s", origin)

	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

func loadClassifier(model llm.LLM, path string) (agentruntime.ScopeClassifier, error) {
	var config struct {
		Instructions  string          `json:"instructions"`
		SchemaName    string          `json:"schema_name"`
		Schema        json.RawMessage `json:"schema"`
		DecisionField string          `json:"decision_field"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("read scope classifier configuration: %w", err)
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF || strings.TrimSpace(config.DecisionField) == "" {
		return nil, errors.New("scope configuration requires a decision field and one JSON object")
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(config.Schema, &schema); err != nil || schema.Type != "object" || schema.Properties[config.DecisionField] == nil {
		return nil, errors.New("scope schema must declare the decision field on its root object")
	}
	return agentruntime.NewJSONScopeClassifier(model, llm.JSONRequest{
		Instructions: config.Instructions,
		SchemaName:   config.SchemaName,
		Schema:       config.Schema,
	}, func(raw json.RawMessage) (agentruntime.ScopeDecision, error) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return "", err
		}
		var decision agentruntime.ScopeDecision
		err := json.Unmarshal(fields[config.DecisionField], &decision)
		return decision, err
	})
}
