#!/usr/bin/env python3
"""Local discovery inspector. Database reads are strictly read-only."""
import argparse
import json
from pathlib import Path
import sqlite3
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlsplit
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parent
STATIC = {"/benchmark": "benchmark.html", "/benchmark.html": "benchmark.html", "/overview": "overview.html", "/overview.html": "overview.html", "/overview.js": "overview.js", "/overview.css": "overview.css", "/product-dell.svg": "product-dell.svg", "/product-hp.svg": "product-hp.svg", "/product-thinkpad.svg": "product-thinkpad.svg", "/": "index.html", "/index.html": "index.html", "/app.js": "app.js", "/styles.css": "styles.css", "/style.css": "style.css", "/favicon.svg": "favicon.svg"}
MIME = {".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".svg": "image/svg+xml"}


class InspectorError(Exception):
    def __init__(self, status, code, message):
        self.status, self.code, self.message = status, code, message


def decoded(value, default):
    try:
        return json.loads(value) if value else default
    except (ValueError, TypeError):
        return default


def intent_view(raw):
    if not isinstance(raw, dict):
        return None
    intent = raw.get("Intent")
    if not isinstance(intent, dict):
        return None
    location = intent.get("Location") or {}
    return {"isBuying": intent.get("IsBuyingRequest", False), "confidence": intent.get("Confidence"),
            "itemQuery": intent.get("ItemQuery", ""), "qty": intent.get("Qty"),
            "city": location.get("City", ""), "budgetIDR": intent.get("BudgetIDR"),
            "requirements": [{"name": r.get("Name", ""), "value": r.get("Value", "")}
                             for r in (intent.get("Requirements") or []) if isinstance(r, dict)]}


def result_view(raw):
    if not isinstance(raw, dict):
        return {}
    # The inspector needs counts/outcomes, not arbitrary stored payload fields.
    return {key: raw[key] for key in ("runId", "requestsScanned", "listingsScanned", "truncated", "warnings") if key in raw} | {
        "matches": [{key: match[key] for key in ("matchId", "leadId", "matchScore", "notificationStatus", "leadState") if key in match}
                    for match in raw.get("matches", []) if isinstance(match, dict)]}


def read_state(database, seller, run_id=None):
    """Return only allowlisted discovery evidence; never query policy/token tables."""
    uri = Path(database).resolve().as_uri() + "?mode=ro"
    with sqlite3.connect(uri, uri=True, timeout=2) as db:
        db.row_factory = sqlite3.Row
        db.execute("PRAGMA query_only=ON")
        # A single read transaction keeps run/query/post panels consistent.
        db.execute("BEGIN")
        if not db.execute("SELECT 1 FROM sellers WHERE id=?", (seller,)).fetchone():
            raise InspectorError(404, "SELLER_NOT_FOUND", "Seller was not found")
        history = db.execute("SELECT id,status,created_at,finished_at FROM discovery_runs WHERE seller_id=? ORDER BY created_at DESC,id DESC LIMIT 20", (seller,)).fetchall()
        runs = [{"id": r["id"], "status": r["status"], "createdAt": r["created_at"], "finishedAt": r["finished_at"]} for r in history]
        selected = None
        if run_id:
            selected = db.execute("SELECT id,status,created_at,finished_at,result_json FROM discovery_runs WHERE seller_id=? AND id=?", (seller, run_id)).fetchone()
            if not selected:
                raise InspectorError(404, "RUN_NOT_FOUND", "Run was not found for this seller")
        elif history:
            selected = db.execute("SELECT id,status,created_at,finished_at,result_json FROM discovery_runs WHERE seller_id=? AND id=?", (seller, history[0]["id"])).fetchone()
        now = int(time.time() * 1000)
        inventory = []
        for item in db.execute("SELECT i.id,i.name,i.list_price_idr,i.physical_stock,i.attributes_json,i.active,COALESCE((SELECT SUM(r.quantity) FROM reservations r WHERE r.seller_id=i.seller_id AND r.item_id=i.id AND r.status='active' AND r.expires_at>?),0) AS reserved FROM inventory_items i WHERE i.seller_id=? ORDER BY i.id", (now, seller)):
            inventory.append({"id": item["id"], "name": item["name"], "priceIDR": item["list_price_idr"], "stock": item["physical_stock"], "reserved": item["reserved"], "available": item["physical_stock"] - item["reserved"], "attributes": decoded(item["attributes_json"], {}), "active": bool(item["active"])})
        data = {"run": None, "runs": runs, "inventory": inventory, "queries": [], "posts": [], "matches": [], "leads": []}
        if not selected:
            return data
        run_id = selected["id"]
        data["run"] = {"id": run_id, "status": selected["status"], "createdAt": selected["created_at"], "finishedAt": selected["finished_at"], "result": result_view(decoded(selected["result_json"], {}))}
        associations = db.execute("SELECT query_id,post_id FROM discovery_query_posts WHERE run_id=? ORDER BY query_id,post_id", (run_id,)).fetchall()
        query_posts, post_queries = {}, {}
        for association in associations:
            query_posts.setdefault(association["query_id"], []).append(association["post_id"])
            post_queries.setdefault(association["post_id"], []).append(association["query_id"])
        for q in db.execute("SELECT id,query,source_item_ids_json,pages_fetched FROM discovery_queries WHERE run_id=? ORDER BY ordinal,id", (run_id,)):
            data["queries"].append({"id": q["id"], "text": q["query"], "itemIds": decoded(q["source_item_ids_json"], []), "postIds": query_posts.get(q["id"], []), "pagesFetched": q["pages_fetched"]})
        post_ids = {}
        for p in db.execute("SELECT id,platform,external_post_id,post_json,matching_status,intent_json,decision_code FROM discovery_posts WHERE run_id=? ORDER BY created_at,id", (run_id,)):
            original = decoded(p["post_json"], {})
            post_ids[(p["platform"], p["external_post_id"])] = p["id"]
            data["posts"].append({"id": p["id"], "platform": p["platform"], "externalPostId": p["external_post_id"],
                "buyerId": original.get("BuyerID", ""), "threadId": original.get("ThreadID", ""), "text": original.get("Text", ""),
                "createdAt": original.get("CreatedAt"), "sourceCity": (original.get("Location") or {}).get("City", ""),
                "matchingStatus": p["matching_status"], "decisionCode": p["decision_code"], "queryIds": post_queries.get(p["id"], []),
                "intent": intent_view(decoded(p["intent_json"], None))})
        lead_ids = set()
        for m in db.execute("SELECT id,platform,external_post_id,item_id,score,reason,status,decision_reason,lead_id FROM discovery_matches WHERE run_id=? ORDER BY created_at,id", (run_id,)):
            data["matches"].append({"id": m["id"], "postId": post_ids.get((m["platform"], m["external_post_id"])), "itemId": m["item_id"], "score": m["score"], "reason": m["reason"], "status": m["status"], "decisionReason": m["decision_reason"], "leadId": m["lead_id"]})
            if m["lead_id"]:
                lead_ids.add(m["lead_id"])
        for lead_id in sorted(lead_ids):
            lead = db.execute("SELECT id,item_id,state,post_id,platform FROM leads WHERE id=? AND seller_id=?", (lead_id, seller)).fetchone()
            if not lead:
                continue
            n = db.execute("SELECT event_id,status,attempts,accepted_at FROM runtime_notifications WHERE lead_id=?", (lead_id,)).fetchone()
            data["leads"].append({"id": lead["id"], "itemId": lead["item_id"], "state": lead["state"], "postId": lead["post_id"], "platform": lead["platform"],
                "notification": {"eventId": n["event_id"], "status": n["status"], "attempts": n["attempts"], "acceptedAt": n["accepted_at"]} if n else None})
        return data


def valid_identity(value):
    return isinstance(value, str) and bool(value) and value.strip() == value and len(value.encode("utf-8")) <= 200


def backend_reachable(backend, seller):
    try:
        request = Request(backend + "/api/inventory?limit=1", headers={"X-Seller-ID": seller})
        with urlopen(request, timeout=1) as response:
            return response.status == 200
    except (URLError, TimeoutError, OSError):
        return False


def make_server(database, backend="http://127.0.0.1:8082", port=8766, runtime_label="P1 context probe"):
    parsed = urlsplit(backend)
    if parsed.scheme != "http" or parsed.hostname not in ("127.0.0.1", "localhost", "::1") or parsed.username or parsed.password or parsed.path not in ("", "/") or parsed.query or parsed.fragment:
        raise ValueError("backend must be a local HTTP origin")
    backend = backend.rstrip("/")

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def send_json(self, status, value):
            payload = json.dumps(value, ensure_ascii=False, allow_nan=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(payload)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.end_headers()
            self.wfile.write(payload)

        def fail(self, status, code, message):
            self.send_json(status, {"error": {"code": code, "message": message}})

        def do_GET(self):
            parsed = urlsplit(self.path)
            if parsed.path == "/inspect/state":
                params = parse_qs(parsed.query)
                seller = params.get("seller", ["seller_demo"])[0]
                run_id = params.get("run", [None])[0]
                if not valid_identity(seller) or run_id is not None and not valid_identity(run_id):
                    self.fail(400, "INVALID_ARGUMENT", "Valid seller and run identifiers are required")
                    return
                try:
                    data = read_state(database, seller, run_id)
                    data["backend"] = {"url": backend, "reachable": backend_reachable(backend, seller), "runtimeLabel": runtime_label}
                    self.send_json(200, data)
                except InspectorError as error:
                    self.fail(error.status, error.code, error.message)
                except (sqlite3.Error, OSError, ValueError, TypeError):
                    self.fail(503, "INSPECTOR_UNAVAILABLE", "Discovery evidence is unavailable; start the local backend first")
                return
            filename = STATIC.get(parsed.path)
            if not filename:
                self.fail(404, "NOT_FOUND", "View resource not found")
                return
            target = ROOT / filename
            try:
                payload = target.read_bytes()
            except OSError:
                self.fail(404, "NOT_FOUND", "View resource not found")
                return
            self.send_response(200)
            self.send_header("Content-Type", MIME.get(target.suffix, "application/octet-stream"))
            self.send_header("Content-Length", str(len(payload)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.end_headers()
            self.wfile.write(payload)

        def do_POST(self):
            if urlsplit(self.path).path != "/inspect/discover":
                self.fail(404, "NOT_FOUND", "Inspector operation not found")
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if length <= 0 or length > 4096 or self.headers.get_content_type() != "application/json":
                    raise ValueError()
                value = json.loads(self.rfile.read(length))
                if not isinstance(value, dict) or set(value) != {"sellerId"} or not valid_identity(value["sellerId"]):
                    raise ValueError()
            except (ValueError, UnicodeError, TypeError):
                self.fail(400, "INVALID_ARGUMENT", "Request must contain a valid sellerId only")
                return
            request = Request(backend + "/api/matches/search", data=json.dumps(value).encode("utf-8"), headers={"Content-Type": "application/json"}, method="POST")
            try:
                with urlopen(request, timeout=100) as response:
                    status, payload = response.status, response.read(4 << 20)
            except HTTPError as error:
                status, payload = error.code, error.read(4 << 20)
            except (URLError, TimeoutError, OSError):
                self.fail(503, "BACKEND_UNAVAILABLE", "The local discovery backend could not complete the request")
                return
            try:
                response_value = json.loads(payload)
            except (ValueError, UnicodeError):
                self.fail(502, "INVALID_BACKEND_RESPONSE", "The local backend returned an unreadable response")
                return
            self.send_json(status, response_value)

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.daemon_threads = True
    return server


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", default="/tmp/reverse-marketplace-local-demo.sqlite")
    parser.add_argument("--backend", default="http://127.0.0.1:8082")
    parser.add_argument("--port", default=8766, type=int)
    parser.add_argument("--runtime-label", default="P1 context probe")
    args = parser.parse_args()
    server = make_server(args.db, args.backend, args.port, args.runtime_label)
    print(f"Discovery backstage: http://127.0.0.1:{server.server_port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
