package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
)

func TestScopeConfigRequiresDeclaredDecisionField(t *testing.T) {
	model, err := llm.NewOpenAIClient(llm.Config{APIKey: "unused", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, schema string
		valid        bool
	}{
		{name: "null", schema: "null"},
		{name: "wrong field", schema: `{"type":"object","properties":{"other":{"type":"string"}}}`},
		{name: "valid", schema: `{"type":"object","properties":{"scope":{"type":"string"}}}`, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scope.json")
			input := `{"instructions":"Test only","schema_name":"test","schema":` + test.schema + `,"decision_field":"scope"}`
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			classifier, err := loadClassifier(model, path)
			if test.valid && (err != nil || classifier == nil) {
				t.Fatalf("valid classifier profile was rejected: %v", err)
			}
			if !test.valid && (err == nil || classifier != nil) {
				t.Fatal("invalid classifier profile was accepted")
			}
		})
	}
}
