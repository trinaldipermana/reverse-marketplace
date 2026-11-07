// Command demo serves only P2 inventory/policy APIs for local frontend integration.
// It is not a second application server or a complete marketplace demo.
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

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	addr := flag.String("addr", "127.0.0.1:8082", "local API listen address")
	path := flag.String("db", filepath.Join(os.TempDir(), "reverse-marketplace-p2-inventory.sqlite"), "SQLite file path")
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
	service, err := mockpos.New(db, mockpos.Options{ReservedStock: mockpos.NoReservations})
	if err != nil {
		return err
	}
	if err = service.SeedDemo(ctx); err != nil {
		return err
	}
	server := &http.Server{Addr: *addr, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	fmt.Printf("P2 inventory API: http://%s; demo seller=%s; no checkout/reservations configured\n", *addr, mockpos.DemoSellerID)
	select {
	case err := <-done:
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
