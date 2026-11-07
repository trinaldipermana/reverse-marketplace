package mockpos_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

func setup(t *testing.T, reader mockpos.ReservedStock) (*mockpos.Service, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := mockpos.OpenSQLite(ctx, filepath.Join(t.TempDir(), "pos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if reader == nil {
		reader = mockpos.NoReservations
	}
	s, err := mockpos.New(db, mockpos.Options{ReservedStock: reader})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	return s, db
}
func code(t *testing.T, err error, want contracts.Code) {
	t.Helper()
	var failure *contracts.Error
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}
func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func ptr[T any](v T) *T { return &v }

func TestSeedPaginationAndSellerIsolation(t *testing.T) {
	ctx := context.Background()
	s, db := setup(t, nil)
	if err := s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM stock_movements`); n != 3 {
		t.Fatalf("seed duplicated movements: %d", n)
	}
	first, err := s.List(ctx, mockpos.DemoSellerID, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("page 1: %+v %v", first, err)
	}
	second, err := s.List(ctx, mockpos.DemoSellerID, first.NextCursor, 2)
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("page 2: %+v %v", second, err)
	}
	if second.Items[0].ID <= first.Items[1].ID {
		t.Fatal("pagination repeated/reordered IDs")
	}
	_, err = s.List(ctx, mockpos.DemoSellerID, "bad!", 20)
	code(t, err, contracts.InvalidArgument)
	_, err = s.List(ctx, "missing", "", 20)
	code(t, err, contracts.NotFound)
	_, err = s.Get(ctx, "other", first.Items[0].ID)
	code(t, err, contracts.NotFound)
	_, err = s.GetRules(ctx, "other", first.Items[0].ID)
	code(t, err, contracts.NotFound)
	// SQL constraints/foreign keys remain enabled on the injected pool.
	if _, err = db.Exec(`INSERT INTO sellers(id,name,created_at) VALUES('other','Other',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO seller_policies VALUES('ghost',0,1000,10,20,1,0)`); err == nil {
		t.Fatal("foreign key disabled")
	}
}

func TestCreatePatchAndReplay(t *testing.T) {
	ctx := context.Background()
	s, db := setup(t, nil)
	in := mockpos.CreateItem{Name: "New laptop", ListPriceIDR: 6_000_000, InitialStock: 3, Attributes: map[string]string{"ram_gb": "16"}}
	item, err := s.Create(ctx, mockpos.DemoSellerID, "create", in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Create(ctx, mockpos.DemoSellerID, "create", in)
	if err != nil || item.ID != again.ID {
		t.Fatalf("create replay: %+v %v", again, err)
	}
	in.InitialStock = 4
	_, err = s.Create(ctx, mockpos.DemoSellerID, "create", in)
	code(t, err, contracts.IdempotencyConflict)
	patch := mockpos.PatchItem{ExpectedVersion: 1, Name: ptr("Edited laptop"), Active: ptr(false)}
	edited, err := s.Patch(ctx, mockpos.DemoSellerID, item.ID, "edit", patch)
	if err != nil || edited.Version != 2 || edited.Active {
		t.Fatalf("edit: %+v %v", edited, err)
	}
	replay, err := s.Patch(ctx, mockpos.DemoSellerID, item.ID, "edit", patch)
	if err != nil || replay.Version != 2 {
		t.Fatalf("patch replay: %+v %v", replay, err)
	}
	_, err = s.Patch(ctx, mockpos.DemoSellerID, item.ID, "stale", patch)
	code(t, err, contracts.VersionConflict)
	if n := count(t, db, `SELECT count(*) FROM stock_movements WHERE item_id=?`, item.ID); n != 1 {
		t.Fatalf("edits changed ledger: %d", n)
	}
	// Reseeding must not reset user changes to an existing demo item.
	_, err = s.Adjust(ctx, mockpos.DemoSellerID, "item_thinkpad", "consume", mockpos.AdjustStock{ExpectedVersion: 1, Delta: -1, Reason: "POS sale"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	existing, _ := s.Get(ctx, mockpos.DemoSellerID, "item_thinkpad")
	if existing.PhysicalStock != 1 {
		t.Fatal("seed reset stock")
	}
}

func TestReservationsAndExpiryAffectAdjustments(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	reader := func(ctx context.Context, tx *sql.Tx, seller, item string, at time.Time) (int, error) {
		var held int
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(qty),0) FROM test_holds WHERE seller=? AND item=? AND expires>?`, seller, item, at.UnixMilli()).Scan(&held)
		return held, err
	}
	s, db := setup(t, nil)
	if _, err := db.Exec(`CREATE TABLE test_holds(seller TEXT,item TEXT,qty INTEGER,expires INTEGER)`); err != nil {
		t.Fatal(err)
	}
	s, err := mockpos.New(db, mockpos.Options{Now: func() time.Time { return now }, ReservedStock: reader})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO test_holds VALUES(?,?,1,?)`, mockpos.DemoSellerID, "item_thinkpad", now.Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	item, err := s.Get(ctx(), mockpos.DemoSellerID, "item_thinkpad")
	if err != nil || item.AvailableStock != 1 || item.ReservedStock != 1 {
		t.Fatalf("availability: %+v %v", item, err)
	}
	req := mockpos.AdjustStock{ExpectedVersion: 1, Delta: -2, Reason: "manual reduction"}
	_, err = s.Adjust(ctx(), mockpos.DemoSellerID, item.ID, "held", req)
	code(t, err, contracts.InvalidState)
	if n := count(t, db, `SELECT count(*) FROM pos_actions WHERE action_key='held'`); n != 0 {
		t.Fatal("blocked action committed")
	}
	now = now.Add(2 * time.Minute)
	item, err = s.Adjust(ctx(), mockpos.DemoSellerID, item.ID, "after-expiry", req)
	if err != nil || item.PhysicalStock != 0 || item.AvailableStock != 0 {
		t.Fatalf("expired hold: %+v %v", item, err)
	}
}
func ctx() context.Context { return context.Background() }

func TestStockAdjustmentIdempotencyAndConcurrentVersions(t *testing.T) {
	s, db := setup(t, nil)
	req := mockpos.AdjustStock{ExpectedVersion: 1, Delta: -1, Reason: "POS sale"}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"a", "b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := s.Adjust(ctx(), mockpos.DemoSellerID, "item_thinkpad", key, req)
			results <- err
		}(key)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			code(t, err, contracts.VersionConflict)
			conflicts++
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	item, err := s.Get(ctx(), mockpos.DemoSellerID, "item_thinkpad")
	if err != nil || item.PhysicalStock != 1 || item.Version != 2 {
		t.Fatalf("lost update: %+v %v", item, err)
	}
	if n := count(t, db, `SELECT count(*) FROM stock_movements WHERE item_id='item_thinkpad' AND kind='adjustment'`); n != 1 {
		t.Fatalf("ledger count %d", n)
	}
	req.ExpectedVersion = 2
	req.Delta = 1
	first, err := s.Adjust(ctx(), mockpos.DemoSellerID, item.ID, "replay", req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Adjust(ctx(), mockpos.DemoSellerID, item.ID, "replay", req)
	if err != nil || first.Version != again.Version || first.PhysicalStock != again.PhysicalStock {
		t.Fatalf("replay changed stock: %+v %v", again, err)
	}
	req.Delta = 2
	_, err = s.Adjust(ctx(), mockpos.DemoSellerID, item.ID, "replay", req)
	code(t, err, contracts.IdempotencyConflict)
}

func TestLedgerFailureRollsBackStockAndAction(t *testing.T) {
	s, db := setup(t, nil)
	if _, err := db.Exec(`CREATE TRIGGER fail_movement BEFORE INSERT ON stock_movements WHEN NEW.kind='adjustment' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Adjust(ctx(), mockpos.DemoSellerID, "item_thinkpad", "rollback", mockpos.AdjustStock{ExpectedVersion: 1, Delta: -1, Reason: "test"})
	if err == nil {
		t.Fatal("expected ledger failure")
	}
	item, err := s.Get(ctx(), mockpos.DemoSellerID, "item_thinkpad")
	if err != nil || item.PhysicalStock != 2 || item.Version != 1 {
		t.Fatalf("partial mutation: %+v %v", item, err)
	}
	if n := count(t, db, `SELECT count(*) FROM pos_actions WHERE action_key='rollback'`); n != 0 {
		t.Fatal("failed mutation saved replay result")
	}
}

func TestPolicyValidationAndCancellation(t *testing.T) {
	s, _ := setup(t, nil)
	_, err := s.PatchPolicy(ctx(), mockpos.DemoSellerID, "invalid", mockpos.PatchPolicy{ExpectedVersion: 1, MaxDiscountBPS: ptr(10001)})
	code(t, err, contracts.InvalidArgument)
	_, err = s.PatchPolicy(ctx(), mockpos.DemoSellerID, "floor", mockpos.PatchPolicy{ExpectedVersion: 1, FloorPriceIDR: ptr(int64(8_000_000))})
	code(t, err, contracts.InvalidArgument)
	p, err := s.PatchPolicy(ctx(), mockpos.DemoSellerID, "policy", mockpos.PatchPolicy{ExpectedVersion: 1, MessageCap: ptr(15)})
	if err != nil || p.Version != 2 || p.MessageCap != 15 {
		t.Fatalf("policy: %+v %v", p, err)
	}
	rules, err := s.GetRules(ctx(), mockpos.DemoSellerID, "item_thinkpad")
	if err != nil || rules.MessageCap != 15 {
		t.Fatalf("rules: %+v %v", rules, err)
	}
	cancelCtx, cancel := context.WithCancel(ctx())
	cancel()
	_, err = s.Adjust(cancelCtx, mockpos.DemoSellerID, "item_thinkpad", "cancel", mockpos.AdjustStock{ExpectedVersion: 1, Delta: -1, Reason: "test"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestActionRecordFailureRollsBackLedgerAndItem(t *testing.T) {
	s, db := setup(t, nil)
	if _, err := db.Exec(`CREATE TRIGGER fail_action BEFORE INSERT ON pos_actions BEGIN SELECT RAISE(ABORT,'action write failed'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Adjust(ctx(), mockpos.DemoSellerID, "item_thinkpad", "failed-action", mockpos.AdjustStock{ExpectedVersion: 1, Delta: -1, Reason: "test"})
	if err == nil {
		t.Fatal("expected action write failure")
	}
	item, err := s.Get(ctx(), mockpos.DemoSellerID, "item_thinkpad")
	if err != nil || item.PhysicalStock != 2 || item.Version != 1 {
		t.Fatalf("partial stock commit: %+v %v", item, err)
	}
	if n := count(t, db, `SELECT count(*) FROM stock_movements WHERE kind='adjustment'`); n != 0 {
		t.Fatal("ledger committed without replay result")
	}
}

func TestInventorySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persistent.sqlite")
	db, err := mockpos.OpenSQLite(ctx(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	s, err := mockpos.New(db, mockpos.Options{ReservedStock: mockpos.NoReservations})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = s.SeedDemo(ctx()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	req := mockpos.AdjustStock{ExpectedVersion: 1, Delta: -1, Reason: "persisted sale"}
	if _, err = s.Adjust(ctx(), mockpos.DemoSellerID, "item_thinkpad", "persist", req); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = mockpos.OpenSQLite(ctx(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrations.Apply(ctx(), db); err != nil {
		t.Fatal(err)
	}
	s, err = mockpos.New(db, mockpos.Options{ReservedStock: mockpos.NoReservations})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SeedDemo(ctx()); err != nil {
		t.Fatal(err)
	}
	item, err := s.Adjust(ctx(), mockpos.DemoSellerID, "item_thinkpad", "persist", req)
	if err != nil || item.PhysicalStock != 1 || item.Version != 2 {
		t.Fatalf("restart lost stock/idempotency: %+v %v", item, err)
	}
	if n := count(t, db, `SELECT count(*) FROM stock_movements WHERE item_id='item_thinkpad'`); n != 2 {
		t.Fatalf("restart/seed duplicated ledger: %d", n)
	}
}

func TestReservationDependencyIsMandatory(t *testing.T) {
	s, db := setup(t, nil)
	_ = s
	if _, err := mockpos.New(db, mockpos.Options{}); err == nil {
		t.Fatal("missing authoritative stock dependency accepted")
	}
	s, err := mockpos.New(db, mockpos.Options{ReservedStock: func(context.Context, *sql.Tx, string, string, time.Time) (int, error) {
		return 0, errors.New("reservation store failed")
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Adjust(ctx(), mockpos.DemoSellerID, "item_thinkpad", "dependency", mockpos.AdjustStock{ExpectedVersion: 1, Delta: -1, Reason: "test"})
	if err == nil {
		t.Fatal("failed reservation read allowed adjustment")
	}
	if n := count(t, db, `SELECT physical_stock FROM inventory_items WHERE id='item_thinkpad'`); n != 2 {
		t.Fatal("dependency failure changed stock")
	}
}
