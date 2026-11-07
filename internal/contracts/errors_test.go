package contracts_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
)

func TestErrorRetainsCauseWithoutDisplayingIt(t *testing.T) {
	err := fmt.Errorf("load context: %w", &contracts.Error{
		Code: contracts.DependencyUnavailable, Message: "commerce unavailable", Retryable: true,
		Cause: fmt.Errorf("private connection detail: %w", context.DeadlineExceeded),
	})
	var failure *contracts.Error
	if !errors.As(err, &failure) || failure.Code != contracts.DependencyUnavailable || !failure.Retryable {
		t.Fatalf("classification lost: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("deadline cause lost")
	}
	if strings.Contains(err.Error(), "private connection") {
		t.Fatal("internal cause leaked into safe error text")
	}
}
