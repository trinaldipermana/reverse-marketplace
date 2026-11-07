package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDemoLifecycleResetPreservesRunWhenReseedFails(t *testing.T) {
	reseedErr := errors.New("seed unavailable")
	originalCanceled := make(chan struct{})
	calls := 0
	lifecycle, err := newDemoLifecycle(t.Context(), func(context.Context) (*demoRun, error) {
		calls++
		if calls > 1 {
			return nil, reseedErr
		}
		return &demoRun{
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, "original state")
			}),
			cancel: func() { close(originalCanceled) },
			stop:   func() {},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lifecycle.Close)

	if err := lifecycle.Reset(t.Context()); !errors.Is(err, reseedErr) {
		t.Fatalf("Reset error = %v, want %v", err, reseedErr)
	}
	select {
	case <-originalCanceled:
		t.Fatal("reseed failure canceled the current run")
	default:
	}
	assertDemoResponse(t, lifecycle, http.StatusOK, "original state")
}

func TestDemoLifecycleResetFencesInFlightRequests(t *testing.T) {
	started := make(chan struct{})
	requestCanceled := make(chan struct{})
	finish := make(chan struct{})
	runCanceled := make(chan struct{})
	runStopped := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(finish) }) }
	generation := 0
	lifecycle, err := newDemoLifecycle(t.Context(), func(context.Context) (*demoRun, error) {
		generation++
		id := generation
		var mutations atomic.Int32
		return &demoRun{
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if id == 1 && r.URL.Path == "/slow" {
					close(started)
					<-r.Context().Done()
					close(requestCanceled)
					<-finish
					mutations.Add(1)
				}
				fmt.Fprintf(w, "run %d, mutations %d", id, mutations.Load())
			}),
			cancel: func() {
				if id == 1 {
					close(runCanceled)
				}
			},
			stop: func() {
				if id == 1 {
					close(runStopped)
				}
			},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lifecycle.Close)
	t.Cleanup(release)

	oldResponse := httptest.NewRecorder()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		lifecycle.ServeHTTP(oldResponse, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/slow", nil))
	}()
	waitForDemoSignal(t, started, "old request to start")

	resetResult := make(chan error, 1)
	go func() { resetResult <- lifecycle.Reset(t.Context()) }()
	if err := awaitDemoReset(t, resetResult); err != nil {
		t.Fatal(err)
	}
	waitForDemoSignal(t, runCanceled, "old runtime cancellation")
	waitForDemoSignal(t, requestCanceled, "old request cancellation")
	select {
	case <-runStopped:
		t.Fatal("old run stopped while its HTTP request was still active")
	default:
	}

	assertDemoResponse(t, lifecycle, http.StatusOK, "run 2, mutations 0")
	release()
	waitForDemoSignal(t, requestDone, "old request to finish")
	waitForDemoSignal(t, runStopped, "old run cleanup")
	if got := oldResponse.Body.String(); got != "run 1, mutations 1" {
		t.Fatalf("old response = %q", got)
	}
	assertDemoResponse(t, lifecycle, http.StatusOK, "run 2, mutations 0")
}

func TestDemoLifecycleCanceledResetDiscardsStagedRun(t *testing.T) {
	staging := make(chan struct{})
	stagedCanceled := make(chan struct{})
	stagedStopped := make(chan struct{})
	finishCleanup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(finishCleanup) }) }
	calls := 0
	lifecycle, err := newDemoLifecycle(t.Context(), func(ctx context.Context) (*demoRun, error) {
		calls++
		if calls == 1 {
			return &demoRun{
				handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprint(w, "original state")
				}),
				cancel: func() {},
				stop:   func() {},
			}, nil
		}
		close(staging)
		<-ctx.Done()
		return &demoRun{
			handler: http.NotFoundHandler(),
			cancel:  func() { close(stagedCanceled) },
			stop: func() {
				<-finishCleanup
				close(stagedStopped)
			},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lifecycle.Close)
	t.Cleanup(release)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resetResult := make(chan error, 1)
	go func() { resetResult <- lifecycle.Reset(ctx) }()
	waitForDemoSignal(t, staging, "reseed to start")
	cancel()

	if err := awaitDemoReset(t, resetResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reset error = %v, want context cancellation", err)
	}
	waitForDemoSignal(t, stagedCanceled, "staged runtime cancellation")
	assertDemoResponse(t, lifecycle, http.StatusOK, "original state")
	release()
	waitForDemoSignal(t, stagedStopped, "staged run cleanup")
}

func TestDemoLifecycleCloseCancelsReseed(t *testing.T) {
	staging := make(chan struct{})
	initialStopped := make(chan struct{})
	var calls atomic.Int32
	lifecycle, err := newDemoLifecycle(t.Context(), func(ctx context.Context) (*demoRun, error) {
		if calls.Add(1) == 1 {
			return &demoRun{
				handler: http.NotFoundHandler(),
				cancel:  func() {},
				stop:    func() { close(initialStopped) },
			}, nil
		}
		close(staging)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}

	resetResult := make(chan error, 1)
	go func() { resetResult <- lifecycle.Reset(t.Context()) }()
	waitForDemoSignal(t, staging, "reseed to start")
	closed := make(chan struct{})
	go func() {
		lifecycle.Close()
		close(closed)
	}()
	waitForDemoSignal(t, closed, "lifecycle to close")
	if err := awaitDemoReset(t, resetResult); err == nil {
		t.Fatal("Reset succeeded during Close")
	}
	waitForDemoSignal(t, initialStopped, "initial run cleanup")
	assertDemoResponse(t, lifecycle, http.StatusServiceUnavailable, "demo is closed\n")
	if err := lifecycle.Reset(t.Context()); err == nil {
		t.Fatal("Reset succeeded after Close")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("factory ran %d times, want 2", got)
	}
	lifecycle.Close()
}

func TestDemoLifecycleResetWaitingForAnotherReseedRespectsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finish := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(finish) }) }
		var calls atomic.Int32
		lifecycle, err := newDemoLifecycle(t.Context(), func(context.Context) (*demoRun, error) {
			if calls.Add(1) == 2 {
				<-finish
			}
			return &demoRun{handler: http.NotFoundHandler(), cancel: func() {}, stop: func() {}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer lifecycle.Close()
		defer release()

		first := make(chan error, 1)
		go func() { first <- lifecycle.Reset(t.Context()) }()
		synctest.Wait()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		second := make(chan error, 1)
		go func() { second <- lifecycle.Reset(ctx) }()
		synctest.Wait()
		if got := calls.Load(); got != 2 {
			t.Fatalf("factory ran %d times, want 2", got)
		}
		select {
		case err := <-second:
			t.Fatalf("second reset finished while the first was still reseeding: %v", err)
		default:
		}

		cancel()
		if err := awaitDemoReset(t, second); !errors.Is(err, context.Canceled) {
			t.Fatalf("Reset error = %v, want context cancellation", err)
		}
		release()
		if err := awaitDemoReset(t, first); err != nil {
			t.Fatal(err)
		}
	})
}

func waitForDemoSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func awaitDemoReset(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Reset")
		return nil
	}
}

func assertDemoResponse(t *testing.T, handler http.Handler, status int, body string) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if response.Code != status || response.Body.String() != body {
		t.Fatalf("response = %d %q, want %d %q", response.Code, response.Body, status, body)
	}
}
