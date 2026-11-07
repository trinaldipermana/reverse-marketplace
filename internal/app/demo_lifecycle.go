package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

var errDemoClosed = errors.New("demo is closed")

type demoRun struct {
	handler http.Handler
	// cancel signals the run's background workers. stop waits for them and
	// releases the run's resources after its admitted HTTP requests return.
	cancel context.CancelFunc
	stop   func()
}

// The factory uses ctx for setup only and cleans up partial runs on error.
// Each successful run owns its stores and background work so retiring it
// cannot write to its successor.
type demoRunFactory func(context.Context) (*demoRun, error)

type demoGeneration struct {
	run            *demoRun
	requestContext context.Context
	cancelRequests context.CancelFunc
	requests       sync.WaitGroup
}

func prepareDemoGeneration(ctx context.Context, factory demoRunFactory) (*demoGeneration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	run, err := factory(ctx)
	if err != nil {
		return nil, err
	}
	requestContext, cancelRequests := context.WithCancel(context.Background())
	return &demoGeneration{run: run, requestContext: requestContext, cancelRequests: cancelRequests}, nil
}

func (g *demoGeneration) cancel() {
	g.cancelRequests()
	g.run.cancel()
}

func (g *demoGeneration) stop() {
	g.requests.Wait()
	g.run.stop()
}

type demoLifecycle struct {
	factory        demoRunFactory
	resetGate      chan struct{} // Serialize reseeding without blocking HTTP.
	shutdown       context.Context
	cancelShutdown context.CancelFunc

	mu      sync.Mutex
	current *demoGeneration
	retired sync.WaitGroup
}

func newDemoLifecycle(ctx context.Context, factory demoRunFactory) (*demoLifecycle, error) {
	initial, err := prepareDemoGeneration(ctx, factory)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		initial.cancel()
		initial.stop()
		return nil, err
	}
	shutdown, cancelShutdown := context.WithCancel(context.Background())
	return &demoLifecycle{
		factory:        factory,
		resetGate:      make(chan struct{}, 1),
		shutdown:       shutdown,
		cancelShutdown: cancelShutdown,
		current:        initial,
	}, nil
}

func (l *demoLifecycle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	generation := l.current
	if generation == nil {
		l.mu.Unlock()
		http.Error(w, errDemoClosed.Error(), http.StatusServiceUnavailable)
		return
	}
	generation.requests.Add(1)
	l.mu.Unlock()
	defer generation.requests.Done()

	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(generation.requestContext, cancel)
	defer cancel()
	defer stop()
	generation.run.handler.ServeHTTP(w, r.WithContext(ctx))
}

func (l *demoLifecycle) Reset(ctx context.Context) error {
	select {
	case l.resetGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-l.shutdown.Done():
		return errDemoClosed
	}
	defer func() { <-l.resetGate }()
	if l.shutdown.Err() != nil {
		return errDemoClosed
	}

	setupContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.shutdown, cancel)
	defer cancel()
	defer stop()
	next, err := prepareDemoGeneration(setupContext, l.factory)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current == nil {
		l.retire(next)
		return errDemoClosed
	}
	if err := setupContext.Err(); err != nil {
		l.retire(next)
		return err
	}
	previous := l.current
	l.current = next
	l.retire(previous)
	return nil
}

// Close stops admission and waits for any active reseed and run cleanup.
func (l *demoLifecycle) Close() {
	l.mu.Lock()
	if l.current != nil {
		previous := l.current
		l.current = nil
		l.cancelShutdown()
		l.retire(previous)
	}
	l.mu.Unlock()

	// Wait for a racing reset to retire any replacement it staged.
	l.resetGate <- struct{}{}
	<-l.resetGate
	l.retired.Wait()
}

// retire requires mu to be held and the generation to be off current,
// so there can be no new requests for it while cleanup waits.
func (l *demoLifecycle) retire(generation *demoGeneration) {
	generation.cancel()
	l.retired.Go(generation.stop)
}
