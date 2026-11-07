package mockpos

import (
	"context"
	"encoding/json"
)

const DemoSellerID = "seller_demo"

// SeedDemo adds missing demo data once; rerunning never resets live stock,
// overrides edits, deletes records, or creates duplicate opening movements.
func (s *Service) SeedDemo(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT INTO sellers(id,name,city,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, DemoSellerID, "Jakarta Laptop Store", "Jakarta", now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO seller_policies(seller_id,floor_price_idr,max_discount_bps,message_cap,rate_limit_per_minute,version,updated_at) VALUES(?,5400000,1000,10,20,1,?) ON CONFLICT(seller_id) DO NOTHING`, DemoSellerID, now); err != nil {
		return err
	}
	items := []struct {
		ID, Name, RAM string
		Price         int64
		Stock         int
	}{
		{"item_thinkpad", "Used ThinkPad T480", "16", 5_800_000, 2},
		{"item_dell", "Used Dell Latitude 5490", "16", 6_200_000, 1},
		{"item_hp", "Used HP EliteBook", "8", 7_000_000, 1},
	}
	for _, i := range items {
		attrs, _ := json.Marshal(map[string]string{"ram_gb": i.RAM, "storage_gb": "512", "condition": "used"})
		res, err := tx.ExecContext(ctx, `INSERT INTO inventory_items(id,seller_id,name,description,attributes_json,list_price_idr,physical_stock,city,active,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'Jakarta',1,1,?,?) ON CONFLICT(id) DO NOTHING`, i.ID, DemoSellerID, i.Name, "Used laptop; see recorded specifications", string(attrs), i.Price, i.Stock, now, now)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			if err = s.movement(ctx, tx, DemoSellerID, i.ID, i.Stock, i.Stock, "initial", "demo opening stock"); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
