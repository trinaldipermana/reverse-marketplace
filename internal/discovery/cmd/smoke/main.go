// Command smoke exercises live structured discovery on synthetic inventory/posts.
// It queues a real P1 context-load probe, not a buyer-facing conversation.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/discovery"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := llm.ConfigFromEnv()
	if err != nil {
		return err
	}
	client, err := llm.NewOpenAIClient(cfg)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "reverse-marketplace-discovery-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, err := mockpos.OpenSQLite(ctx, filepath.Join(directory, "smoke.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = migrations.Apply(ctx, db); err != nil {
		return err
	}
	core, err := commerce.New(db, commerce.Options{})
	if err != nil {
		return err
	}
	if err = core.Inventory().SeedDemo(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	posts, err := mock.NewPostStore(
		domain.Post{PostRef: domain.PostRef{Platform: "mock", ExternalPostID: "buying"}, BuyerID: "buyer_smoke", ThreadID: "thread_buying", Text: "Butuh laptop bekas / used laptop untuk coding, RAM minimal 16GB, budget under 6 juta rupiah, di Jakarta minggu ini.", CreatedAt: now, Location: domain.Location{City: "Jakarta"}},
		domain.Post{PostRef: domain.PostRef{Platform: "mock", ExternalPostID: "selling"}, BuyerID: "seller_smoke", ThreadID: "thread_selling", Text: "Dijual laptop bekas ThinkPad 16GB di Jakarta. Ini iklan penjualan, bukan mencari barang.", CreatedAt: now, Location: domain.Location{City: "Jakarta"}},
	)
	if err != nil {
		return err
	}
	loaded := make(chan error, 4)
	worker, err := agentruntime.NewWorker(8, func(ctx context.Context, event agentruntime.Event) error {
		_, err := agentruntime.LoadLeadContext(ctx, core, event.LeadID)
		loaded <- err
		return err
	})
	if err != nil {
		return err
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- worker.Run(workerCtx) }()
	defer func() { stop(); <-done }()
	adapter, err := discovery.NewModelAdapter(client)
	if err != nil {
		return err
	}
	service, err := discovery.New(db, core, posts, adapter, adapter, worker, discovery.Options{})
	if err != nil {
		return err
	}
	result, err := service.Discover(ctx, mockpos.DemoSellerID)
	if err != nil {
		return err
	}
	if len(result.Matches) != 1 || result.Matches[0].NotificationStatus != "accepted" {
		var decisions string
		rows, queryErr := db.QueryContext(ctx, `SELECT decision_reason FROM discovery_matches ORDER BY id`)
		if queryErr == nil {
			for rows.Next() {
				var code string
				if rows.Scan(&code) == nil {
					decisions += code + ","
				}
			}
			rows.Close()
		}
		return fmt.Errorf("expected one accepted buying lead; got scanned=%d matches=%d warnings=%d decision_codes=%s", result.RequestsScanned, len(result.Matches), len(result.Warnings), decisions)
	}
	select {
	case err := <-loaded:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	repeat, err := service.Discover(ctx, mockpos.DemoSellerID)
	if err != nil {
		return err
	}
	if len(repeat.Matches) != 1 || repeat.Matches[0].LeadID != result.Matches[0].LeadID {
		return fmt.Errorf("repeat discovery changed lead identity")
	}
	var events int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_notifications`).Scan(&events); err != nil {
		return err
	}
	if events != 1 {
		return fmt.Errorf("duplicate runtime events")
	}
	fmt.Printf("Live discovery OK; model=%s; scanned=%d; leads=%d; one persisted runtime event; P1 context loaded; repeat deduplicated\n", cfg.Model, result.RequestsScanned, len(result.Matches))
	return nil
}
