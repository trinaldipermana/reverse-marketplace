package actionlog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func validEntry() Entry {
	return Entry{EventID: "event-1", LeadID: "lead-1", SellerID: "seller-1", Kind: KindTool, Status: StatusOK, Tool: ToolMakeOffer, Code: CodeOK}
}

func TestRecorderStoresOnlySafeMetadata(t *testing.T) {
	sink, err := NewMemorySink(2)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	entry := validEntry()
	if err := recorder.Record(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	got := sink.Entries()
	if len(got) != 1 || got[0] != entry || got[0].Summary() == "" {
		t.Fatalf("record=%+v", got)
	}
	encoded, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "arguments", "checkout", "raw_error", "FloorPrice", "MaxDiscount", "reason"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("unexpected field in audit projection: %s", forbidden)
		}
	}
	got[0].Code = CodeOutOfStock
	if sink.Entries()[0] != entry {
		t.Fatal("read result changed sink contents")
	}
}

func TestRecorderRejectsUnknownMetadataBeforeSink(t *testing.T) {
	for _, mutate := range []func(*Entry){
		func(e *Entry) { e.Code = ResultCode("private minimum 5400000") },
		func(e *Entry) { e.Tool = ToolName("https://checkout/private-token") },
		func(e *Entry) { e.EventID = "" },
		func(e *Entry) { e.LeadID = "" },
		func(e *Entry) { e.Kind = Kind("private prompt") },
		func(e *Entry) { e.Status = StatusBlocked },
	} {
		calls := 0
		recorder, _ := NewRecorder(sinkFunc(func(context.Context, Entry) error { calls++; return nil }))
		entry := validEntry()
		mutate(&entry)
		err := recorder.Record(context.Background(), entry)
		if !errors.Is(err, ErrInvalidEntry) || calls != 0 {
			t.Fatalf("error=%v sink calls=%d", err, calls)
		}
		if strings.Contains(err.Error(), "5400000") || strings.Contains(err.Error(), "private-token") {
			t.Fatal("rejected metadata leaked")
		}
	}
}

func TestRecorderPreservesOpaqueIdentifiers(t *testing.T) {
	sink, _ := NewMemorySink(1)
	recorder, _ := NewRecorder(sink)
	entry := validEntry()
	entry.EventID = "mock:reply:123"
	entry.LeadID = "platform/lead:123"
	entry.SellerID = "seller:" + strings.Repeat("x", 160)
	if err := recorder.Record(context.Background(), entry); err != nil {
		t.Fatalf("valid opaque identity rejected: %v", err)
	}
	if got := sink.Entries(); len(got) != 1 || got[0] != entry {
		t.Fatalf("opaque identities changed: %+v", got)
	}
}

type sinkFunc func(context.Context, Entry) error

func (f sinkFunc) Append(ctx context.Context, e Entry) error { return f(ctx, e) }

func TestRecorderReportsWriteFailureWithoutRawText(t *testing.T) {
	cause := errors.New("private prompt and https://checkout/private-token")
	recorder, _ := NewRecorder(sinkFunc(func(context.Context, Entry) error { return cause }))
	err := recorder.Record(context.Background(), validEntry())
	if !errors.Is(err, ErrWriteFailed) || !errors.Is(err, cause) {
		t.Fatalf("write failure not recognizable: %v", err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("error text echoed sink data")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	recorder, _ = NewRecorder(sinkFunc(func(context.Context, Entry) error { calls++; return nil }))
	if err := recorder.Record(ctx, validEntry()); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled write error=%v calls=%d", err, calls)
	}
}

func TestMemorySinkIsBoundedAndConcurrent(t *testing.T) {
	sink, _ := NewMemorySink(8)
	recorder, _ := NewRecorder(sink)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := recorder.Record(context.Background(), validEntry()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(sink.Entries()) != 8 {
		t.Fatal("lost concurrent record")
	}
	if err := recorder.Record(context.Background(), validEntry()); !errors.Is(err, ErrCapacity) {
		t.Fatalf("full sink error=%v", err)
	}
	if len(sink.Entries()) != 8 {
		t.Fatal("full sink evicted records")
	}
}

func TestEntrySummariesAndStatusBindings(t *testing.T) {
	for _, entry := range []Entry{
		{EventID: "event-1", LeadID: "lead-1", SellerID: "seller-1", Kind: KindDiscovery, Status: StatusOK, Code: CodeOK},
		{EventID: "event-1", LeadID: "lead-1", SellerID: "seller-1", Kind: KindGuardrail, Status: StatusBlocked, Tool: ToolMakeOffer, Code: CodePriceBelowMinimum},
		{EventID: "event-1", LeadID: "lead-1", SellerID: "seller-1", Kind: KindState, Status: StatusOK, Code: CodeOK},
		{EventID: "event-1", Kind: KindError, Status: StatusFailed, Code: CodeTimedOut},
	} {
		if err := entry.Validate(); err != nil || entry.Summary() == "" {
			t.Fatalf("invalid expected event: %+v %v", entry, err)
		}
	}
}
