CREATE TABLE sellers(id TEXT PRIMARY KEY, name TEXT NOT NULL, city TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
CREATE TABLE seller_policies(
 seller_id TEXT PRIMARY KEY REFERENCES sellers(id), floor_price_idr INTEGER NOT NULL CHECK(floor_price_idr>=0),
 max_discount_bps INTEGER NOT NULL CHECK(max_discount_bps BETWEEN 0 AND 10000), message_cap INTEGER NOT NULL CHECK(message_cap>0),
 rate_limit_per_minute INTEGER NOT NULL CHECK(rate_limit_per_minute>0), version INTEGER NOT NULL CHECK(version>0), updated_at INTEGER NOT NULL);
CREATE TABLE inventory_items(
 id TEXT PRIMARY KEY, seller_id TEXT NOT NULL REFERENCES sellers(id), name TEXT NOT NULL CHECK(length(trim(name))>0),
 description TEXT NOT NULL DEFAULT '', attributes_json TEXT NOT NULL CHECK(json_valid(attributes_json)),
 list_price_idr INTEGER NOT NULL CHECK(list_price_idr>0), physical_stock INTEGER NOT NULL CHECK(physical_stock BETWEEN 0 AND 1000000),
 city TEXT NOT NULL DEFAULT '', active INTEGER NOT NULL CHECK(active IN(0,1)), version INTEGER NOT NULL CHECK(version>0),
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE(seller_id,id));
CREATE INDEX inventory_seller_page ON inventory_items(seller_id,id);
CREATE TABLE stock_movements(
 id TEXT PRIMARY KEY, seller_id TEXT NOT NULL, item_id TEXT NOT NULL, quantity_delta INTEGER NOT NULL CHECK(quantity_delta!=0),
 quantity_after INTEGER NOT NULL CHECK(quantity_after>=0), kind TEXT NOT NULL CHECK(kind IN('initial','adjustment','payment')),
 reason TEXT NOT NULL, created_at INTEGER NOT NULL, FOREIGN KEY(seller_id,item_id) REFERENCES inventory_items(seller_id,id));
CREATE INDEX stock_item_history ON stock_movements(seller_id,item_id,created_at,id);
CREATE TABLE pos_actions(
 seller_id TEXT NOT NULL REFERENCES sellers(id), action_key TEXT NOT NULL, operation TEXT NOT NULL, request_hash TEXT NOT NULL,
 result_json TEXT NOT NULL CHECK(json_valid(result_json)), created_at INTEGER NOT NULL, PRIMARY KEY(seller_id,action_key));
