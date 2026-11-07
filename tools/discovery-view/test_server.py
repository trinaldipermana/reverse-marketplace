import json
from pathlib import Path
import sqlite3
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import server


class InspectorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.database = str(Path(self.tmp.name) / 'discovery.sqlite')
        repo = Path(__file__).resolve().parents[2]
        with sqlite3.connect(self.database) as db:
            for migration in sorted((repo / 'migrations').glob('*.sql')):
                db.executescript(migration.read_text())
            db.execute("INSERT INTO sellers VALUES('seller_demo','Demo','Jakarta',1)")
            db.execute("INSERT INTO sellers VALUES('other','Other','Bandung',1)")
            db.execute("INSERT INTO seller_policies VALUES('seller_demo',13456789,1234,10,20,1,1)")
            db.execute("INSERT INTO inventory_items VALUES('item','seller_demo','Laptop','Description','{\"ram_gb\":\"16\"}',5800000,2,'Jakarta',1,1,1,1)")
            db.execute("INSERT INTO leads VALUES('lead','seller_demo','mock','post','buyer','thread','item','active','{}','match','[]','',1,1,1)")
            db.execute("INSERT INTO runtime_notifications VALUES('event','lead','accepted',1,NULL,3,NULL,NULL,2,3)")
            db.execute("INSERT INTO commerce_actions VALUES('seller_demo','secret-action','checkout','hash','{\"token\":\"SHOULD_NOT_APPEAR\"}',1)")
            result = json.dumps({'runId':'run','requestsScanned':1,'listingsScanned':1,'matches':[{'leadId':'lead','token':'SHOULD_NOT_APPEAR'}],'private':'SHOULD_NOT_APPEAR'})
            db.execute("INSERT INTO discovery_runs VALUES('run','seller_demo','completed',?,1,4)", (result,))
            db.execute("INSERT INTO discovery_runs VALUES('foreign-run','other','completed','{}',1,4)")
            db.execute("INSERT INTO discovery_queries VALUES('query','run',0,'laptop','laptop','[\"item\"]','',1,1)")
            post = json.dumps({'Platform':'mock','ExternalPostID':'post','BuyerID':'buyer','ThreadID':'thread','Text':'Need a laptop','CreatedAt':'2026-11-07T00:00:00Z','Location':{'City':'Jakarta'}})
            intent = json.dumps({'Intent':{'IsBuyingRequest':True,'Confidence':.96,'ItemQuery':'laptop','Qty':1,'Location':{'City':'Jakarta'},'BudgetIDR':6000000,'Requirements':[{'Name':'ram_gb_min','Value':'16'}]}})
            db.execute("INSERT INTO discovery_posts VALUES('snapshot','run','mock','post',?,'completed',?,'EXTRACTED',1)", (post,intent))
            db.execute("INSERT INTO discovery_query_posts VALUES('run','query','snapshot')")
            db.execute("INSERT INTO discovery_matches VALUES('match','run','mock','post','item',.99,'RAM meets request','selected','','lead',1)")

    def test_safe_projection_and_read_only(self):
        before = Path(self.database).read_bytes()
        state = server.read_state(self.database, 'seller_demo')
        self.assertEqual(state['queries'][0]['postIds'], ['snapshot'])
        self.assertEqual(state['posts'][0]['queryIds'], ['query'])
        self.assertEqual(state['posts'][0]['intent']['requirements'], [{'name':'ram_gb_min','value':'16'}])
        self.assertEqual(state['matches'][0]['postId'], 'snapshot')
        self.assertEqual(state['leads'][0]['notification']['eventId'], 'event')
        encoded = json.dumps(state)
        for secret in ('SHOULD_NOT_APPEAR','13456789','floor_price_idr','token','request_hash'):
            self.assertNotIn(secret, encoded)
        self.assertEqual(before, Path(self.database).read_bytes())

    def test_seller_scoping_and_empty_history(self):
        state = server.read_state(self.database, 'other')
        self.assertEqual(state['run']['id'], 'foreign-run')
        self.assertEqual(state['inventory'], [])
        with self.assertRaises(server.InspectorError) as failure:
            server.read_state(self.database, 'seller_demo', 'foreign-run')
        self.assertEqual(failure.exception.status, 404)
        with self.assertRaises(server.InspectorError):
            server.read_state(self.database, 'missing')
        with sqlite3.connect(self.database) as db:
            db.execute("INSERT INTO sellers VALUES('empty','Empty','Jakarta',1)")
        self.assertIsNone(server.read_state(self.database, 'empty')['run'])

    def test_http_proxy_validation_and_static_allowlist(self):
        received = []
        class Backend(BaseHTTPRequestHandler):
            def log_message(self,*_): pass
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                received.append((self.path,body))
                payload = b'{"error":{"code":"DEMO_CONFLICT","message":"already running"}}'
                self.send_response(409);self.send_header('Content-Length',str(len(payload)));self.end_headers();self.wfile.write(payload)
        backend = ThreadingHTTPServer(('127.0.0.1',0),Backend)
        thread = threading.Thread(target=backend.serve_forever,daemon=True);thread.start()
        inspector = server.make_server(self.database, f'http://127.0.0.1:{backend.server_port}', 0)
        worker = threading.Thread(target=inspector.serve_forever,daemon=True);worker.start()
        try:
            origin = f'http://127.0.0.1:{inspector.server_port}'
            for path in ('/.env','/server.py','/../server.py','/discovery.sqlite'):
                with self.assertRaises(HTTPError) as error: urlopen(origin+path)
                self.assertEqual(error.exception.code,404)
            for body in ({'seller_id':'seller_demo'},{'sellerId':' seller_demo'},{'sellerId':'seller_demo','backend':'other'}):
                request=Request(origin+'/inspect/discover',data=json.dumps(body).encode(),headers={'Content-Type':'application/json'},method='POST')
                with self.assertRaises(HTTPError) as error:urlopen(request)
                self.assertEqual(error.exception.code,400)
            self.assertEqual(received,[])
            request=Request(origin+'/inspect/discover',data=b'{"sellerId":"seller_demo"}',headers={'Content-Type':'application/json'},method='POST')
            with self.assertRaises(HTTPError) as error:urlopen(request)
            self.assertEqual(error.exception.code,409)
            self.assertEqual(json.load(error.exception)['error']['code'],'DEMO_CONFLICT')
            self.assertEqual(received,[('/api/matches/search',{'sellerId':'seller_demo'})])
        finally:
            inspector.shutdown();inspector.server_close();worker.join()
            backend.shutdown();backend.server_close();thread.join()


if __name__ == '__main__':
    unittest.main()
