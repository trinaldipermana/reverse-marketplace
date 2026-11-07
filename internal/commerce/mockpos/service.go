package mockpos

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

const itemColumns = `id,seller_id,name,description,attributes_json,list_price_idr,physical_stock,city,active,version`
const maxPrice int64 = 1_000_000_000_000

func validateItem(in CreateItem) error {
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || len(in.Description) > 10000 || len(in.City) > 200 || in.ListPriceIDR <= 0 || in.ListPriceIDR > maxPrice || in.InitialStock < 0 || in.InitialStock > 1_000_000 || len(in.Attributes) > 50 {
		return problem(contracts.InvalidArgument, "invalid inventory fields")
	}
	for k, v := range in.Attributes {
		if strings.TrimSpace(k) == "" || len(k) > 100 || len(v) > 500 {
			return problem(contracts.InvalidArgument, "invalid item attribute")
		}
	}
	return nil
}

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var i Item
	var attrs string
	err := row.Scan(&i.ID, &i.SellerID, &i.Name, &i.Description, &attrs, &i.ListPriceIDR, &i.PhysicalStock, &i.City, &i.Active, &i.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return i, problem(contracts.NotFound, "item not found")
	}
	if err != nil {
		return i, err
	}
	if err = json.Unmarshal([]byte(attrs), &i.Attributes); err != nil {
		return i, err
	}
	if i.Attributes == nil {
		i.Attributes = map[string]string{}
	}
	i.Currency = "IDR"
	return i, nil
}
func (s *Service) availability(ctx context.Context, tx *sql.Tx, i Item) (Item, error) {
	held, err := s.reserved(ctx, tx, i.SellerID, i.ID, s.now())
	if err != nil {
		return Item{}, err
	}
	if held < 0 || held > i.PhysicalStock {
		return Item{}, problem(contracts.InvalidState, "inconsistent stock allocation")
	}
	i.ReservedStock = held
	i.AvailableStock = i.PhysicalStock - held
	return i, nil
}
func (s *Service) item(ctx context.Context, tx *sql.Tx, seller, id string) (Item, error) {
	i, err := scanItem(tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM inventory_items WHERE seller_id=? AND id=?`, seller, id))
	if err != nil {
		return i, err
	}
	return s.availability(ctx, tx, i)
}
func (s *Service) Get(ctx context.Context, seller, id string) (Item, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	i, err := s.item(ctx, tx, seller, id)
	if err != nil {
		return i, err
	}
	return i, tx.Commit()
}
func (s *Service) GetItem(ctx context.Context, seller, id string) (domain.Item, error) {
	i, err := s.Get(ctx, seller, id)
	return i.domainItem(), err
}

func (s *Service) List(ctx context.Context, seller, cursor string, limit int) (Page, error) {
	page := Page{Items: []Item{}}
	if limit < 1 || limit > 100 {
		return page, problem(contracts.InvalidArgument, "limit must be between 1 and 100")
	}
	after := ""
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) > 200 || len(raw) == 0 {
			return page, problem(contracts.InvalidArgument, "invalid cursor")
		}
		after = string(raw)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sellers WHERE id=?`, seller).Scan(&exists); err != nil {
		return page, err
	}
	if exists == 0 {
		return page, problem(contracts.NotFound, "seller not found")
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+itemColumns+` FROM inventory_items WHERE seller_id=? AND id>? ORDER BY id LIMIT ?`, seller, after, limit+1)
	if err != nil {
		return page, err
	}
	var items []Item
	for rows.Next() {
		i, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return page, err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(items) > limit {
		items = items[:limit]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(items[len(items)-1].ID))
	}
	for _, i := range items {
		i, err = s.availability(ctx, tx, i)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, i)
	}
	return page, tx.Commit()
}

// mutate obtains a write lock before reading stock/version, then commits ledger,
// mutation, and replay result together. Replays return the original response.
func mutate[T any](ctx context.Context, s *Service, seller, key, operation string, request any, fn func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		return zero, problem(contracts.InvalidArgument, "Idempotency-Key is required and must be at most 200 characters")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return zero, err
	}
	sum := sha256.Sum256(append([]byte(operation+"\n"), raw...))
	fingerprint := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE sellers SET name=name WHERE id=?`, seller)
	if err != nil {
		return zero, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return zero, err
	}
	if n == 0 {
		return zero, problem(contracts.NotFound, "seller not found")
	}
	var previous, body, op string
	err = tx.QueryRowContext(ctx, `SELECT operation,request_hash,result_json FROM pos_actions WHERE seller_id=? AND action_key=?`, seller, key).Scan(&op, &previous, &body)
	if err == nil {
		if previous != fingerprint || op != operation {
			return zero, problem(contracts.IdempotencyConflict, "idempotency key was used for different arguments")
		}
		if err = json.Unmarshal([]byte(body), &zero); err != nil {
			return zero, err
		}
		return zero, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	out, err := fn(tx)
	if err != nil {
		return zero, err
	}
	data, err := json.Marshal(out)
	if err != nil {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO pos_actions(seller_id,action_key,operation,request_hash,result_json,created_at) VALUES(?,?,?,?,?,?)`, seller, key, operation, fingerprint, string(data), s.now().UnixMilli()); err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return out, nil
}
func (s *Service) Create(ctx context.Context, seller, key string, in CreateItem) (Item, error) {
	if err := validateItem(in); err != nil {
		return Item{}, err
	}
	if in.Attributes == nil {
		in.Attributes = map[string]string{}
	}
	return mutate(ctx, s, seller, key, "create_item", in, func(tx *sql.Tx) (Item, error) {
		p, err := policy(ctx, tx, seller)
		if err != nil {
			return Item{}, err
		}
		if in.ListPriceIDR < p.FloorPriceIDR {
			return Item{}, problem(contracts.InvalidArgument, "list price conflicts with seller policy")
		}
		id := s.id()
		attrs, err := json.Marshal(in.Attributes)
		if err != nil {
			return Item{}, err
		}
		now := s.now().UnixMilli()
		_, err = tx.ExecContext(ctx, `INSERT INTO inventory_items(id,seller_id,name,description,attributes_json,list_price_idr,physical_stock,city,active,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,1,1,?,?)`, id, seller, strings.TrimSpace(in.Name), in.Description, string(attrs), in.ListPriceIDR, in.InitialStock, in.City, now, now)
		if err != nil {
			return Item{}, err
		}
		if in.InitialStock > 0 {
			if err = s.movement(ctx, tx, seller, id, in.InitialStock, in.InitialStock, "initial", "opening stock"); err != nil {
				return Item{}, err
			}
		}
		return s.item(ctx, tx, seller, id)
	})
}
func (s *Service) Patch(ctx context.Context, seller, id, key string, in PatchItem) (Item, error) {
	if in.ExpectedVersion < 1 || in.Name == nil && in.Description == nil && in.Attributes == nil && in.ListPriceIDR == nil && in.City == nil && in.Active == nil {
		return Item{}, problem(contracts.InvalidArgument, "expected_version and editable fields are required")
	}
	request := struct {
		ID    string
		Patch PatchItem
	}{id, in}
	return mutate(ctx, s, seller, key, "patch_item", request, func(tx *sql.Tx) (Item, error) {
		i, err := s.item(ctx, tx, seller, id)
		if err != nil {
			return i, err
		}
		if i.Version != in.ExpectedVersion {
			return i, problem(contracts.VersionConflict, "item changed; reload before editing")
		}
		if in.Name != nil {
			i.Name = *in.Name
		}
		if in.Description != nil {
			i.Description = *in.Description
		}
		if in.Attributes != nil {
			i.Attributes = *in.Attributes
		}
		if in.ListPriceIDR != nil {
			i.ListPriceIDR = *in.ListPriceIDR
		}
		if in.City != nil {
			i.City = *in.City
		}
		if in.Active != nil {
			i.Active = *in.Active
		}
		if err = validateItem(CreateItem{Name: i.Name, Description: i.Description, Attributes: i.Attributes, ListPriceIDR: i.ListPriceIDR, InitialStock: i.PhysicalStock, City: i.City}); err != nil {
			return Item{}, err
		}
		p, err := policy(ctx, tx, seller)
		if err != nil {
			return Item{}, err
		}
		if i.Active && i.ListPriceIDR < p.FloorPriceIDR {
			return Item{}, problem(contracts.InvalidArgument, "list price conflicts with seller policy")
		}
		if i.Attributes == nil {
			i.Attributes = map[string]string{}
		}
		attrs, err := json.Marshal(i.Attributes)
		if err != nil {
			return Item{}, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE inventory_items SET name=?,description=?,attributes_json=?,list_price_idr=?,city=?,active=?,version=version+1,updated_at=? WHERE seller_id=? AND id=? AND version=?`, strings.TrimSpace(i.Name), i.Description, string(attrs), i.ListPriceIDR, i.City, i.Active, s.now().UnixMilli(), seller, id, in.ExpectedVersion)
		if err != nil {
			return Item{}, err
		}
		return s.item(ctx, tx, seller, id)
	})
}
func (s *Service) Adjust(ctx context.Context, seller, id, key string, in AdjustStock) (Item, error) {
	if in.ExpectedVersion < 1 || in.Delta == 0 || in.Delta > 1_000_000 || in.Delta < -1_000_000 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 500 {
		return Item{}, problem(contracts.InvalidArgument, "invalid stock adjustment")
	}
	request := struct {
		ID         string
		Adjustment AdjustStock
	}{id, in}
	return mutate(ctx, s, seller, key, "adjust_stock", request, func(tx *sql.Tx) (Item, error) {
		i, err := s.item(ctx, tx, seller, id)
		if err != nil {
			return i, err
		}
		if i.Version != in.ExpectedVersion {
			return Item{}, problem(contracts.VersionConflict, "item changed; reload before adjusting")
		}
		next := i.PhysicalStock + in.Delta
		if next < 0 || next > 1_000_000 {
			return Item{}, problem(contracts.InvalidArgument, "stock would be outside allowed bounds")
		}
		if next < i.ReservedStock {
			return Item{}, problem(contracts.InvalidState, "stock is allocated to active reservations")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE inventory_items SET physical_stock=?,version=version+1,updated_at=? WHERE seller_id=? AND id=? AND version=?`, next, s.now().UnixMilli(), seller, id, in.ExpectedVersion); err != nil {
			return Item{}, err
		}
		if err = s.movement(ctx, tx, seller, id, in.Delta, next, "adjustment", strings.TrimSpace(in.Reason)); err != nil {
			return Item{}, err
		}
		return s.item(ctx, tx, seller, id)
	})
}
func (s *Service) movement(ctx context.Context, tx *sql.Tx, seller, item string, delta, after int, kind, reason string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO stock_movements(id,seller_id,item_id,quantity_delta,quantity_after,kind,reason,created_at) VALUES(?,?,?,?,?,?,?,?)`, s.id(), seller, item, delta, after, kind, reason, s.now().UnixMilli())
	return err
}
