package actionlog

import (
	"context"
	"errors"
	"sync"
)

// Sink owns storage. Append must honor cancellation and report write failure.
// A successful in-memory write does not acknowledge durable event acceptance.
type Sink interface {
	Append(context.Context, Entry) error
}

type Recorder struct{ sink Sink }

var (
	ErrWriteFailed = errors.New("action-log write failed")
	ErrCapacity    = errors.New("action-log memory capacity reached")
)

func NewRecorder(sink Sink) (*Recorder, error) {
	if sink == nil {
		return nil, errors.New("action-log sink is required")
	}
	return &Recorder{sink: sink}, nil
}

// Record validates metadata and returns explicit failure. It does not retry:
// earlier buyer/commerce side effects must not be repeated because logging failed.
func (r *Recorder) Record(ctx context.Context, e Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if err := r.sink.Append(ctx, e); err != nil {
		return writeFailure{cause: err}
	}
	return nil
}

// The caller can inspect the cause for internal diagnostics, but Error never
// echoes it. Never serialize an unwrapped cause into buyer-facing audit data.
type writeFailure struct{ cause error }

func (e writeFailure) Error() string        { return ErrWriteFailed.Error() }
func (e writeFailure) Unwrap() error        { return e.cause }
func (e writeFailure) Is(target error) bool { return target == ErrWriteFailed }

// MemorySink is bounded, concurrent-safe temporary storage. It preserves append
// order without inventing timestamps or a public dashboard response schema.
// A full sink rejects the new entry; it never silently evicts old outcomes.
type MemorySink struct {
	mu       sync.Mutex
	capacity int
	entries  []Entry
}

func NewMemorySink(capacity int) (*MemorySink, error) {
	if capacity < 1 {
		return nil, errors.New("action-log memory capacity must be positive")
	}
	return &MemorySink{capacity: capacity, entries: make([]Entry, 0, capacity)}, nil
}

func (s *MemorySink) Append(ctx context.Context, e Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(s.entries) == s.capacity {
		return ErrCapacity
	}
	s.entries = append(s.entries, e)
	return nil
}

func (s *MemorySink) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.entries...)
}
