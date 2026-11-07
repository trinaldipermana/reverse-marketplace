package mockpos_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
)

func request(t *testing.T, h http.Handler, method, path, seller, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if seller != "" {
		r.Header.Set("X-Seller-ID", seller)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestHTTPInventoryLifecycle(t *testing.T) {
	s, _ := setup(t, nil)
	h := s.Handler()
	seller := mockpos.DemoSellerID
	w := request(t, h, "GET", "/api/inventory?limit=2", seller, "", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var page struct{ Data mockpos.Page }
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Data.Items) != 2 || page.Data.NextCursor == "" {
		t.Fatalf("page: %s %v", w.Body.String(), err)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("floor_price")) {
		t.Fatal("inventory exposed policy")
	}
	create := `{"name":"New coding laptop","list_price_idr":6000000,"initial_stock":2,"attributes":{"ram_gb":"16"},"city":"Jakarta"}`
	w = request(t, h, "POST", "/api/inventory", seller, "create", create)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var item struct{ Data mockpos.Item }
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	path := "/api/inventory/" + item.Data.ID
	w = request(t, h, "PATCH", path, seller, "patch", `{"expected_version":1,"active":false}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"active":false`) {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	adjust := `{"expected_version":2,"delta":-1,"reason":"POS sale"}`
	w = request(t, h, "POST", path+"/stock-adjustments", seller, "sale", adjust)
	if w.Code != 200 {
		t.Fatalf("adjust: %d %s", w.Code, w.Body.String())
	}
	original := w.Body.String()
	w = request(t, h, "POST", path+"/stock-adjustments", seller, "sale", adjust)
	if w.Code != 200 || w.Body.String() != original {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, "GET", "/api/seller/policy", seller, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "floor_price_idr") {
		t.Fatalf("seller policy: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, "PATCH", "/api/seller/policy", seller, "policy", `{"expected_version":1,"message_cap":12}`)
	if w.Code != 200 {
		t.Fatalf("policy edit: %d %s", w.Code, w.Body.String())
	}
}
func TestHTTPRejectsInvalidAndCrossSellerRequests(t *testing.T) {
	s, _ := setup(t, nil)
	h := s.Handler()
	seller := mockpos.DemoSellerID
	tests := []struct {
		name, method, path, seller, key, body string
		status                                int
		code                                  string
	}{
		{"missing seller", "GET", "/api/inventory", "", "", "", 400, "INVALID_ARGUMENT"},
		{"unknown seller", "GET", "/api/inventory", "unknown", "", "", 404, "NOT_FOUND"},
		{"cross seller", "GET", "/api/inventory/item_thinkpad", "other", "", "", 404, "NOT_FOUND"},
		{"bad limit", "GET", "/api/inventory?limit=101", seller, "", "", 400, "INVALID_ARGUMENT"},
		{"bad cursor", "GET", "/api/inventory?cursor=!!", seller, "", "", 400, "INVALID_ARGUMENT"},
		{"unknown field", "POST", "/api/inventory", seller, "key", `{"name":"Laptop","surprise":true}`, 400, "INVALID_ARGUMENT"},
		{"invalid json", "POST", "/api/inventory", seller, "key", `{`, 400, "INVALID_ARGUMENT"},
		{"multiple values", "POST", "/api/inventory", seller, "key", `{} {}`, 400, "INVALID_ARGUMENT"},
		{"missing action key", "POST", "/api/inventory", seller, "", `{"name":"Laptop","list_price_idr":6000000}`, 400, "INVALID_ARGUMENT"},
		{"negative price", "POST", "/api/inventory", seller, "key", `{"name":"Laptop","list_price_idr":-1}`, 400, "INVALID_ARGUMENT"},
		{"empty patch", "PATCH", "/api/inventory/item_thinkpad", seller, "patch", `{}`, 400, "INVALID_ARGUMENT"},
		{"version conflict", "PATCH", "/api/inventory/item_thinkpad", seller, "patch", `{"expected_version":99,"active":false}`, 409, "VERSION_CONFLICT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := request(t, h, tt.method, tt.path, tt.seller, tt.key, tt.body)
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.code) {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestHTTPHidesInternalDatabaseErrors(t *testing.T) {
	s, db := setup(t, nil)
	h := s.Handler()
	db.Close()
	w := request(t, h, "GET", "/api/inventory", mockpos.DemoSellerID, "", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "database is closed") || !strings.Contains(w.Body.String(), "DEPENDENCY_UNAVAILABLE") {
		t.Fatalf("unsafe error: %d %s", w.Code, w.Body.String())
	}
}
