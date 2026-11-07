package actionlog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type failingTraceWriter struct{}

func (failingTraceWriter) Write([]byte) (int, error) { return 0, errors.New("private writer failure") }

type shortTraceWriter struct{}

func (shortTraceWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestWriterSinkShowsSafeOutcomesWithoutOpaqueIDs(t *testing.T) {
	var output bytes.Buffer
	sink, err := NewWriterSink(&output)
	if err != nil {
		t.Fatal(err)
	}
	recorder, _ := NewRecorder(sink)
	entry := Entry{EventID: "private-event\nforged log", LeadID: "https://checkout.invalid/private-token", SellerID: "private-floor-5400000", ToolCallID: "private-call", Kind: KindTool, Tool: ToolSendMessage, Status: StatusOK, Code: CodePending}
	if err := recorder.Record(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	pending := output.String()
	if !strings.Contains(pending, "send_message") || !strings.Contains(pending, "DELIVERY_PENDING") || strings.Contains(pending, "succeeded") {
		t.Fatalf("pending trace=%q", pending)
	}
	entry.Code = CodeOK
	if err := recorder.Record(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "message delivery succeeded") {
		t.Fatal("confirmed delivery absent")
	}
	for _, secret := range []string{"private-", "checkout.invalid", "5400000", "forged log"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("trace leaked %q", secret)
		}
	}
	if strings.Count(output.String(), "\n") != 2 {
		t.Fatal("opaque IDs injected extra trace lines")
	}
}

func TestWriterSinkRejectsInvalidEntriesAndReportsWrites(t *testing.T) {
	if _, err := NewWriterSink(nil); err == nil {
		t.Fatal("accepted absent writer")
	}
	var output bytes.Buffer
	sink, _ := NewWriterSink(&output)
	entry := Entry{EventID: "event", LeadID: "lead", SellerID: "seller", Kind: KindTurnStarted, Status: StatusOK, Code: CodeOK}
	invalid := entry
	invalid.Kind = Kind("private arbitrary text")
	if !errors.Is(sink.Append(t.Context(), invalid), ErrInvalidEntry) || output.Len() != 0 {
		t.Fatal("invalid event reached trace")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(sink.Append(ctx, entry), context.Canceled) || output.Len() != 0 {
		t.Fatal("canceled event reached trace")
	}
	for _, writer := range []io.Writer{failingTraceWriter{}, shortTraceWriter{}} {
		sink, _ := NewWriterSink(writer)
		err := sink.Append(t.Context(), entry)
		if !errors.Is(err, ErrWriteFailed) || strings.Contains(err.Error(), "private") {
			t.Fatalf("write failure=%v", err)
		}
	}
}
