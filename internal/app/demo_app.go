package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/demo"
	"github.com/trinaldirizki/reverse-marketplace/internal/discovery"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	agenttools "github.com/trinaldirizki/reverse-marketplace/internal/runtime/tools"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

const demoPlatform = "mock"

type DemoConfig struct {
	CheckoutOrigin string
	ResetToken     string
	Model          llm.LLM
	// Instructions and Classify bind the owner-approved negotiation and scope assets.
	Instructions string
	Classify     agentruntime.ScopeClassifier
}

type DemoApp struct {
	handler http.Handler
	runs    *demoLifecycle
}

// NewDemoApp constructs the opt-in, single-process mock demo. Serve it on a
// loopback listener and close it after the HTTP server stops admitting requests.
func NewDemoApp(ctx context.Context, cfg DemoConfig) (*DemoApp, error) {
	if strings.TrimSpace(cfg.Instructions) == "" || cfg.Classify == nil {
		return nil, errors.New("demo requires negotiation instructions and a buyer scope classifier")
	}
	model, err := discovery.NewModelAdapter(cfg.Model)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	var runs *demoLifecycle
	if err := demo.RegisterReset(mux, demo.ResetConfig{
		Enabled: true,
		Token:   cfg.ResetToken,
		Reset:   func(ctx context.Context) error { return runs.Reset(ctx) },
	}); err != nil {
		return nil, err
	}
	runs, err = newDemoLifecycle(ctx, func(ctx context.Context) (*demoRun, error) {
		return newMarketplaceRun(ctx, cfg, model)
	})
	if err != nil {
		return nil, err
	}
	mux.Handle("/", runs)
	return &DemoApp{handler: mux, runs: runs}, nil
}

func (a *DemoApp) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.handler.ServeHTTP(w, r) }
func (a *DemoApp) Close()                                           { a.runs.Close() }

func newMarketplaceRun(setup context.Context, cfg DemoConfig, model *discovery.ModelAdapter) (_ *demoRun, err error) {
	dir, err := os.MkdirTemp("", "reverse-marketplace-demo-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	db, err := mockpos.OpenSQLite(setup, filepath.Join(dir, "demo.sqlite"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	if err := migrations.Apply(setup, db); err != nil {
		return nil, err
	}
	core, err := commerce.New(db, commerce.Options{CheckoutOrigin: cfg.CheckoutOrigin})
	if err != nil {
		return nil, err
	}
	if err := core.Inventory().SeedDemo(setup); err != nil {
		return nil, err
	}

	posts, err := mock.NewPostStore()
	if err != nil {
		return nil, err
	}
	threads := mock.NewThreads(posts, nil)
	feed := &demoFeed{PostStore: posts, threads: threads}
	sender, err := marketplace.NewAgentSender(core, threads, threads, cfg.CheckoutOrigin, nil)
	if err != nil {
		return nil, err
	}
	runtime, err := NewP1Runtime(P1RuntimeConfig{
		Commerce:      core,
		Posts:         feed,
		Model:         cfg.Model,
		Send:          sender.SendAgentMessage,
		SendOptions:   agenttools.SendMessageOptions{CheckoutOrigin: cfg.CheckoutOrigin, Channel: contracts.AgentPublicReply},
		QueueCapacity: 64,
		Instructions:  func(context.Context, domain.LeadRecord) (string, error) { return cfg.Instructions, nil },
		ReplyHistory:  demoReplyHistory(threads),
		Classify:      cfg.Classify,
		Redirect:      demoRedirect(core, sender),
	})
	if err != nil {
		return nil, err
	}
	discover, err := discovery.New(db, core, feed, model, model, runtime, discovery.Options{})
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/api/matches/search", discover.Handler())
	mux.Handle("/api/mock/posts", marketplace.NewPostHandler(feed))
	mux.Handle("/api/mock/threads/", marketplace.NewThreadHandler(threads, demoPlatform))
	mux.Handle("/api/", core.Handler())

	// The setup request may return as soon as the run is installed. Give each
	// generation independent cancellation and let lifecycle retire it.
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Go(func() { _ = runtime.Run(ctx) })
	workers.Go(func() { maintainDemoRun(ctx, core, threads, runtime) })
	return &demoRun{
		handler: mux,
		cancel:  cancel,
		stop: func() {
			workers.Wait()
			_ = db.Close()
			_ = os.RemoveAll(dir)
		},
	}, nil
}
