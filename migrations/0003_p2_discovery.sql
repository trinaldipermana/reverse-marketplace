CREATE TABLE discovery_runs (
 id TEXT PRIMARY KEY, seller_id TEXT NOT NULL REFERENCES sellers(id),
 status TEXT NOT NULL CHECK(status IN('running','completed','partial','failed')),
 result_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(result_json)), created_at INTEGER NOT NULL, finished_at INTEGER
);
CREATE UNIQUE INDEX discovery_one_active ON discovery_runs(seller_id) WHERE status='running';
CREATE INDEX discovery_seller_history ON discovery_runs(seller_id,created_at,id);
CREATE TABLE discovery_queries (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES discovery_runs(id), ordinal INTEGER NOT NULL,
 query TEXT NOT NULL, normalized_query TEXT NOT NULL, source_item_ids_json TEXT NOT NULL CHECK(json_valid(source_item_ids_json)),
 next_cursor TEXT NOT NULL DEFAULT '', pages_fetched INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
 UNIQUE(run_id,ordinal), UNIQUE(run_id,normalized_query), UNIQUE(run_id,id)
);
CREATE TABLE discovery_posts (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES discovery_runs(id), platform TEXT NOT NULL, external_post_id TEXT NOT NULL,
 post_json TEXT NOT NULL CHECK(json_valid(post_json)),
 matching_status TEXT NOT NULL DEFAULT 'pending' CHECK(matching_status IN('pending','completed','failed')),
 intent_json TEXT CHECK(intent_json IS NULL OR json_valid(intent_json)), decision_code TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
 UNIQUE(run_id,platform,external_post_id), UNIQUE(run_id,id)
);
CREATE TABLE discovery_query_posts (
 run_id TEXT NOT NULL, query_id TEXT NOT NULL, post_id TEXT NOT NULL,
 PRIMARY KEY(query_id,post_id),
 FOREIGN KEY(run_id,query_id) REFERENCES discovery_queries(run_id,id),
 FOREIGN KEY(run_id,post_id) REFERENCES discovery_posts(run_id,id)
);
CREATE INDEX discovery_post_queries ON discovery_query_posts(post_id,query_id);
CREATE TABLE discovery_matches (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES discovery_runs(id), platform TEXT NOT NULL, external_post_id TEXT NOT NULL,
 item_id TEXT NOT NULL, score REAL NOT NULL CHECK(score>=0 AND score<=1), reason TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN('selected','alternative','rejected')), decision_reason TEXT NOT NULL,
 lead_id TEXT REFERENCES leads(id), created_at INTEGER NOT NULL,
 UNIQUE(run_id,platform,external_post_id,item_id)
);
CREATE UNIQUE INDEX discovery_primary_match ON discovery_matches(run_id,platform,external_post_id) WHERE status='selected';
