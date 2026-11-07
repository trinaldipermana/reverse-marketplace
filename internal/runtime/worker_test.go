package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkerSerializesDeduplicatesAndContinuesAfterFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := make(chan Event, 3)
	release := make(chan struct{})
	failure := errors.New("turn failed")
	worker, err := NewWorker(3, func(ctx context.Context, event Event) error {
		started <- event
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			if event.ID == "first" {
				return failure
			}
			return nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.OnLeadDiscovered(ctx, "first", "lead-1"); err != nil {
		t.Fatal(err)
	}
	if err := worker.OnLeadDiscovered(ctx, "first", "lead-1"); err != nil {
		t.Fatal(err)
	}
	if err := worker.Enqueue(ctx, Event{ID: "second", LeadID: "lead-1", Trigger: TriggerBuyerReply, MessageID: "reply-1"}); err != nil {
		t.Fatal(err)
	}
	if err := worker.OnLeadDiscovered(ctx, "barrier", "lead-2"); err != nil {
		t.Fatal(err)
	}
	if result, ok := worker.Result("first"); !ok || result.Status != EventQueued {
		t.Fatal("accepted event not visible as queued")
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	await := func(want string) {
		t.Helper()
		select {
		case event := <-started:
			if event.ID != want {
				t.Fatalf("started %s, want %s", event.ID, want)
			}
		case <-ctx.Done():
			t.Fatal("worker did not start expected event")
		}
	}
	await("first")
	if result, _ := worker.Result("first"); result.Status != EventRunning {
		t.Fatal("active event not marked running")
	}
	select {
	case event := <-started:
		t.Fatalf("concurrent execution: %+v", event)
	default:
	}
	release <- struct{}{}
	await("second")
	if result, _ := worker.Result("first"); result.Status != EventFailed || !errors.Is(result.Err, failure) {
		t.Fatalf("failure not recorded: %+v", result)
	}
	release <- struct{}{}
	await("barrier")
	if result, _ := worker.Result("second"); result.Status != EventSucceeded || result.Err != nil {
		t.Fatalf("later event failed: %+v", result)
	}
	if err := worker.OnLeadDiscovered(ctx, "first", "lead-1"); err != nil {
		t.Fatal("completed duplicate not accepted idempotently")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error: %v", err)
	}
	if result, _ := worker.Result("barrier"); result.Status != EventCanceled {
		t.Fatalf("running cancellation not recorded: %+v", result)
	}
}

func TestWorkerQueueFullCancellationAndStopped(t *testing.T) {
	worker, err := NewWorker(1, func(context.Context, Event) error { t.Fatal("canceled worker executed an event"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.OnLeadDiscovered(context.Background(), "first", "lead-1"); err != nil {
		t.Fatal(err)
	}
	if err := worker.OnLeadDiscovered(context.Background(), "second", "lead-2"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full queue error: %v", err)
	}
	if _, ok := worker.Result("second"); ok {
		t.Fatal("rejected event was recorded as accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.OnLeadDiscovered(ctx, "second", "lead-2"); !errors.Is(err, context.Canceled) {
		t.Fatalf("enqueue cancellation error: %v", err)
	}
	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run cancellation error: %v", err)
	}
	if result, _ := worker.Result("first"); result.Status != EventCanceled {
		t.Fatalf("queued cancellation not recorded: %+v", result)
	}
	if err := worker.OnLeadDiscovered(context.Background(), "second", "lead-2"); !errors.Is(err, ErrWorkerStopped) {
		t.Fatalf("stopped error: %v", err)
	}
	if err := worker.Run(context.Background()); !errors.Is(err, ErrWorkerAlreadyRun) {
		t.Fatalf("second run error: %v", err)
	}
}

func TestWorkerRejectsInvalidAndConflictingEvents(t *testing.T) {
	if _, err := NewWorker(0, func(context.Context, Event) error { return nil }); err == nil {
		t.Fatal("zero capacity accepted")
	}
	if _, err := NewWorker(1, nil); err == nil {
		t.Fatal("nil runner accepted")
	}
	worker, err := NewWorker(1, func(context.Context, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.OnLeadDiscovered(context.Background(), "", "lead-1"); err == nil {
		t.Fatal("empty event ID accepted")
	}
	if err := worker.OnLeadDiscovered(context.Background(), "event", "lead-1"); err != nil {
		t.Fatal(err)
	}
	if err := worker.OnLeadDiscovered(context.Background(), "event", "lead-2"); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting duplicate error: %v", err)
	}
}
