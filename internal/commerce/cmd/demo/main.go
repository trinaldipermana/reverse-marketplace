// Command demo serves P2 commerce APIs using real inventory reservations.
// Discovery, marketplace messaging, and P1 runtime wiring are separate work.
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
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	addr := flag.String("addr", "127.0.0.1:8082", "API listen address")
	path := flag.String("db", filepath.Join(os.TempDir(), "reverse-marketplace-p2-commerce.sqlite"), "shared SQLite file")
	origin := flag.String("checkout-origin", "http://127.0.0.1:5173", "frontend checkout origin")
	flag.Parse()
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
	service, err := commerce.New(db, commerce.Options{CheckoutOrigin: *origin})
	if err != nil {
		return err
	}
	if err = service.Inventory().SeedDemo(ctx); err != nil {
		return err
	}
	budget := int64(6_000_000)
	if _, _, err = service.CreateLead(ctx, commerce.CreateLeadInput{SellerID: mockpos.DemoSellerID, Platform: "mock", PostID: "post_demo", BuyerID: "buyer_demo", ThreadID: "thread_demo", ItemID: "item_thinkpad", Intent: domain.Intent{IsBuyingRequest: true, Confidence: .96, ItemQuery: "used laptop for coding", Qty: 1, Location: domain.Location{City: "Jakarta"}, BudgetIDR: &budget}, MatchReason: "16GB laptop within the buyer budget"}); err != nil {
		return err
	}
	background, done := context.WithCancel(ctx)
	defer done()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-background.Done():
				return
			case <-ticker.C:
				if _, err := service.ExpireCheckouts(background); err != nil && background.Err() == nil {
					log.Print("checkout expiry pass failed")
				}
			}
		}
	}()
	server := &http.Server{Addr: *addr, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	exit := make(chan error, 1)
	go func() { exit <- server.ListenAndServe() }()
	fmt.Printf("P2 commerce API: http://%s; seller=%s; real reservation accounting enabled\n", *addr, mockpos.DemoSellerID)
	select {
	case err := <-exit:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
