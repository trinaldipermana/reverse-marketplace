package actionlog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// WriterSink is an opt-in readable demo projection of existing audit entries.
// It omits every opaque identity and prints only validated allowlisted fields
// and generated summaries. Never append raw prompts, payloads or error text.
// Successful writes are not durable event or delivery acknowledgements.
type WriterSink struct {
	mu     sync.Mutex
	writer io.Writer
}

func NewWriterSink(writer io.Writer) (*WriterSink, error) {
	if writer == nil {
		return nil, errors.New("action-log writer is required")
	}
	return &WriterSink{writer: writer}, nil
}

func (s *WriterSink) Append(ctx context.Context, entry Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := entry.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	tool := ""
	if entry.Tool != "" {
		tool = " | tool=" + string(entry.Tool)
	}
	line := fmt.Sprintf("[agent] %s | %s | %s%s | %s\n", entry.Kind, entry.Status, entry.Code, tool, entry.Summary())
	n, err := io.WriteString(s.writer, line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return writeFailure{cause: err}
	}
	return nil
}
