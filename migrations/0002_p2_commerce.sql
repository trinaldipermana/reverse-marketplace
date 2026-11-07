CREATE TABLE item_policy_overrides (
 item_id TEXT PRIMARY KEY, seller_id TEXT NOT NULL,
 floor_price_idr INTEGER CHECK(floor_price_idr>=0), max_discount_bps INTEGER CHECK(max_discount_bps BETWEEN 0 AND 10000),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0), updated_at INTEGER NOT NULL,
 FOREIGN KEY(seller_id,item_id) REFERENCES inventory_items(seller_id,id)
);
CREATE TABLE leads (
 id TEXT PRIMARY KEY, seller_id TEXT NOT NULL REFERENCES sellers(id),
 platform TEXT NOT NULL, post_id TEXT NOT NULL, buyer_id TEXT NOT NULL, thread_id TEXT NOT NULL,
 item_id TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN('active','reserved','escalated','paid','lost')),
 intent_json TEXT NOT NULL CHECK(json_valid(intent_json)), match_reason TEXT NOT NULL DEFAULT '',
 alternative_item_ids_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(alternative_item_ids_json)), state_reason TEXT NOT NULL DEFAULT '',
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0), created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 UNIQUE(seller_id,platform,post_id), UNIQUE(seller_id,id),
 FOREIGN KEY(seller_id,item_id) REFERENCES inventory_items(seller_id,id)
);
CREATE INDEX leads_seller_state ON leads(seller_id,state,created_at,id);
CREATE TABLE offers (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
 seller_id TEXT NOT NULL, lead_id TEXT NOT NULL, item_id TEXT NOT NULL,
 price_idr INTEGER NOT NULL CHECK(price_idr>0), qty INTEGER NOT NULL CHECK(qty=1),
 policy_version INTEGER NOT NULL, item_version INTEGER NOT NULL, created_at INTEGER NOT NULL,
 UNIQUE(seller_id,lead_id,id),
 FOREIGN KEY(seller_id,lead_id) REFERENCES leads(seller_id,id),
 FOREIGN KEY(seller_id,item_id) REFERENCES inventory_items(seller_id,id)
);
CREATE INDEX offers_lead_current ON offers(lead_id,seq DESC);
CREATE TABLE reservations (
 id TEXT PRIMARY KEY, seller_id TEXT NOT NULL, lead_id TEXT NOT NULL, item_id TEXT NOT NULL,
 quantity INTEGER NOT NULL CHECK(quantity=1), status TEXT NOT NULL CHECK(status IN('active','committed','released')),
 expires_at INTEGER NOT NULL, released_reason TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 UNIQUE(seller_id,lead_id,id),
 FOREIGN KEY(seller_id,lead_id) REFERENCES leads(seller_id,id),
 FOREIGN KEY(seller_id,item_id) REFERENCES inventory_items(seller_id,id)
);
CREATE UNIQUE INDEX reservations_one_active ON reservations(lead_id) WHERE status='active';
CREATE INDEX reservations_available ON reservations(seller_id,item_id,status,expires_at);
CREATE INDEX reservations_expiry ON reservations(status,expires_at);
CREATE TABLE checkouts (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, seller_id TEXT NOT NULL, lead_id TEXT NOT NULL,
 offer_id TEXT NOT NULL, reservation_id TEXT NOT NULL UNIQUE, item_id TEXT NOT NULL,
 unit_price_idr INTEGER NOT NULL CHECK(unit_price_idr>0), quantity INTEGER NOT NULL CHECK(quantity=1),
 total_idr INTEGER NOT NULL CHECK(total_idr=unit_price_idr*quantity),
 status TEXT NOT NULL CHECK(status IN('pending','paid','expired','cancelled')),
 expires_at INTEGER NOT NULL, token_hash TEXT NOT NULL UNIQUE, token TEXT NOT NULL UNIQUE,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 FOREIGN KEY(seller_id,lead_id,offer_id) REFERENCES offers(seller_id,lead_id,id),
 FOREIGN KEY(seller_id,lead_id,reservation_id) REFERENCES reservations(seller_id,lead_id,id),
 FOREIGN KEY(seller_id,item_id) REFERENCES inventory_items(seller_id,id)
);
CREATE UNIQUE INDEX checkouts_one_pending ON checkouts(lead_id) WHERE status='pending';
CREATE INDEX checkouts_lead_current ON checkouts(lead_id,seq DESC);
CREATE INDEX checkouts_expiry ON checkouts(status,expires_at);
CREATE TABLE payments (
 id TEXT PRIMARY KEY, checkout_id TEXT NOT NULL UNIQUE REFERENCES checkouts(id),
 seller_id TEXT NOT NULL, lead_id TEXT NOT NULL, amount_idr INTEGER NOT NULL CHECK(amount_idr>0),
 quantity INTEGER NOT NULL CHECK(quantity=1), currency TEXT NOT NULL CHECK(currency='IDR'),
 idempotency_key TEXT NOT NULL, created_at INTEGER NOT NULL,
 UNIQUE(seller_id,idempotency_key), FOREIGN KEY(seller_id,lead_id) REFERENCES leads(seller_id,id)
);
CREATE TABLE commerce_actions (
 seller_id TEXT NOT NULL REFERENCES sellers(id), action_key TEXT NOT NULL, operation TEXT NOT NULL,
 request_hash TEXT NOT NULL, result_json TEXT NOT NULL CHECK(json_valid(result_json)), created_at INTEGER NOT NULL,
 PRIMARY KEY(seller_id,action_key)
);
CREATE TABLE runtime_notifications (
 event_id TEXT PRIMARY KEY, lead_id TEXT NOT NULL UNIQUE REFERENCES leads(id),
 status TEXT NOT NULL CHECK(status IN('pending','accepted','failed')), attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
 next_attempt_at INTEGER, accepted_at INTEGER, error_code TEXT, error_message TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX runtime_notifications_dispatch ON runtime_notifications(status,next_attempt_at);
