package runtime

import (
	"context"
	"errors"
	"sync"
)

type Trigger string

const (
	TriggerLeadDiscovered Trigger = "lead_discovered"
	TriggerBuyerReply     Trigger = "buyer_reply"
)

// Event is P1's local work item, not a shared marketplace event contract.
type Event struct {
	ID        string
	LeadID    string
	Trigger   Trigger
	MessageID string
}

type EventStatus string

const (
	EventQueued    EventStatus = "queued"
	EventRunning   EventStatus = "running"
	EventSucceeded EventStatus = "succeeded"
	EventFailed    EventStatus = "failed"
	EventCanceled  EventStatus = "canceled"
)

type EventResult struct {
	Event  Event
	Status EventStatus
	Err    error
}

// EventRunner must honor ctx. Failed events are recorded, not automatically retried.
type EventRunner func(ctx context.Context, event Event) error

var (
	ErrQueueFull        = errors.New("runtime event queue is full")
	ErrWorkerStopped    = errors.New("runtime worker is stopped")
	ErrWorkerAlreadyRun = errors.New("runtime worker can only run once")
	ErrEventConflict    = errors.New("event ID already belongs to different event data")
)

// Worker is a single-process, bounded queue with one synchronous runner. Event
// identities and outcomes live only in memory for this worker's lifetime. A new
// worker or process restart loses them; this is not durable event acceptance.
type Worker struct {
	mu         sync.Mutex
	queue      chan Event
	runner     EventRunner
	results    map[string]EventResult
	started    bool
	stopped    bool
	runContext context.Context
}

func NewWorker(capacity int, runner EventRunner) (*Worker, error) {
	if capacity < 1 {
		return nil, errors.New("worker queue capacity must be positive")
	}
	if runner == nil {
		return nil, errors.New("worker requires an event runner")
	}
	return &Worker{queue: make(chan Event, capacity), runner: runner, results: make(map[string]EventResult)}, nil
}

// OnLeadDiscovered accepts the notification requested in issue #6. The caller
// must commit the lead first and reuse eventID when retrying this notification.
// Nil means accepted (or already accepted), not that an offer was sent.
func (w *Worker) OnLeadDiscovered(ctx context.Context, eventID, leadID string) error {
	return w.Enqueue(ctx, Event{ID: eventID, LeadID: leadID, Trigger: TriggerLeadDiscovered})
}

// Enqueue is nonblocking: a full queue returns ErrQueueFull without claiming the
// event ID, so callers may retry later. Accepted IDs are deduplicated even after
// execution fails; this worker never blindly repeats a failed side effect.
func (w *Worker) Enqueue(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.ID == "" || event.LeadID == "" {
		return errors.New("event and lead IDs are required")
	}
	if event.Trigger != TriggerLeadDiscovered && event.Trigger != TriggerBuyerReply {
		return errors.New("unsupported runtime trigger")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.stopped || (w.runContext != nil && w.runContext.Err() != nil) {
		return ErrWorkerStopped
	}
	if previous, exists := w.results[event.ID]; exists {
		if previous.Event != event {
			return ErrEventConflict
		}
		return nil
	}
	select {
	case w.queue <- event:
		w.results[event.ID] = EventResult{Event: event, Status: EventQueued}
		return nil
	default:
		return ErrQueueFull
	}
}

// Result returns a nonblocking snapshot. No consumer channel or callback can
// stall event execution. Outcomes are retained until this worker is discarded.
func (w *Worker) Result(eventID string) (EventResult, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	result, exists := w.results[eventID]
	return result, exists
}

// Run may be called exactly once. Cancel ctx to stop; it returns only after the
// current runner returns, and never starts another event after cancellation is
// observed. Pending events are marked canceled. The runner must honor ctx for
// shutdown to complete promptly. Queued events can be accepted before Run starts.
func (w *Worker) Run(ctx context.Context) error {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return ErrWorkerAlreadyRun
	}
	w.started = true
	w.runContext = ctx
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.stopped = true
		for id, result := range w.results {
			if result.Status == EventQueued {
				result.Status, result.Err = EventCanceled, ctx.Err()
				w.results[id] = result
			}
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-w.queue:
			if err := ctx.Err(); err != nil {
				return err
			}
			w.mu.Lock()
			w.results[event.ID] = EventResult{Event: event, Status: EventRunning}
			w.mu.Unlock()
			err := w.runner(ctx, event)
			status := EventSucceeded
			if err != nil {
				status = EventFailed
			}
			if ctx.Err() != nil {
				status, err = EventCanceled, ctx.Err()
			}
			w.mu.Lock()
			w.results[event.ID] = EventResult{Event: event, Status: status, Err: err}
			w.mu.Unlock()
		}
	}
}
