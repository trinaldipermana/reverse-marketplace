// Command demo serves synchronous discovery with the live shared JSON client.
// P1 notification runs a context-load probe only; this is not full agent wiring.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/discovery"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	addr := flag.String("addr", "127.0.0.1:8082", "local API listen address")
	path := flag.String("db", filepath.Join(os.TempDir(), "reverse-marketplace-p2-discovery.sqlite"), "shared SQLite file")
	flag.Parse()
	cfg, err := llm.ConfigFromEnv()
	if err != nil {
		return err
	}
	client, err := llm.NewOpenAIClient(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := mockpos.OpenSQLite(ctx, *path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = migrations.Apply(ctx, db); err != nil {
		return err
	}
	core, err := commerce.New(db, commerce.Options{CheckoutOrigin: "http://127.0.0.1:5173"})
	if err != nil {
		return err
	}
	if err = core.Inventory().SeedDemo(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	posts, err := mock.NewPostStore(
		domain.Post{PostRef: domain.PostRef{Platform: "mock", ExternalPostID: "discovery_buying"}, BuyerID: "buyer_discovery", ThreadID: "thread_discovery_buying", Text: "Butuh laptop bekas untuk coding, RAM minimal 16GB, budget under 6 juta rupiah, di Jakarta minggu ini.", CreatedAt: now, Location: domain.Location{City: "Jakarta"}},
		domain.Post{PostRef: domain.PostRef{Platform: "mock", ExternalPostID: "discovery_selling"}, BuyerID: "seller_discovery", ThreadID: "thread_discovery_selling", Text: "Dijual laptop bekas ThinkPad 16GB di Jakarta. Ini iklan penjualan, bukan mencari barang.", CreatedAt: now, Location: domain.Location{City: "Jakarta"}},
	)
	if err != nil {
		return err
	}
	worker, err := agentruntime.NewWorker(100, func(ctx context.Context, event agentruntime.Event) error {
		_, err := agentruntime.LoadLeadContext(ctx, core, event.LeadID)
		if err == nil {
			log.Print("P1 notification context probe completed; no buyer message sent")
		}
		return err
	})
	if err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()
	defer func() { cancel(); <-workerDone }()
	adapter, err := discovery.NewModelAdapter(client)
	if err != nil {
		return err
	}
	discover, err := discovery.New(db, core, posts, adapter, adapter, worker, discovery.Options{})
	if err != nil {
		return err
	}
	if err = discover.RecoverInterruptedRuns(ctx); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/matches/search", discover.Handler())
	mux.Handle("/api/mock/posts", marketplace.NewPostHandler(posts))
	mux.Handle("/api/", core.Handler())
	server := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 30 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.ListenAndServe() }()
	fmt.Printf("P2 discovery API: http://%s; seller=%s; model=%s; P1 context probe only\n", *addr, mockpos.DemoSellerID, cfg.Model)
	select {
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		return server.Shutdown(shutdown)
	}
}
