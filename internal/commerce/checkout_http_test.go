package commerce

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func checkoutRequest(h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestCheckoutHTTPReadAndPay(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead", "offer", "item_dell", 6_000_000)
	c, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "create")
	if err != nil {
		t.Fatal(err)
	}
	token := checkoutToken(t, c)
	path := "/api/checkouts/" + token
	handler := f.s.CheckoutHandler()
	w := checkoutRequest(handler, "GET", path, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), "floor_price") {
		t.Fatalf("unsafe read %d %s", w.Code, w.Body.String())
	}
	var view struct{ Data CheckoutView }
	if err = json.Unmarshal(w.Body.Bytes(), &view); err != nil || view.Data.Status != domain.CheckoutPending {
		t.Fatalf("view %s %v", w.Body.String(), err)
	}
	w = checkoutRequest(handler, "POST", path+"/pay", "pay", `{}`)
	if w.Code != 200 {
		t.Fatalf("pay %d %s", w.Code, w.Body.String())
	}
	original := w.Body.String()
	w = checkoutRequest(handler, "POST", path+"/pay", "pay", "")
	if w.Code != 200 || w.Body.String() != original {
		t.Fatalf("pay replay %d %s", w.Code, w.Body.String())
	}
	w = checkoutRequest(handler, "GET", path, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"paid"`) {
		t.Fatalf("paid view %d %s", w.Code, w.Body.String())
	}
}
func TestCheckoutHTTPRejectsCommercialArgumentsAndExpiredPay(t *testing.T) {
	f := checkoutSetup(t)
	checkoutLeadOffer(t, f, "lead", "offer", "item_dell", 6_000_000)
	c, err := f.s.CreateCheckout(context.Background(), "lead", "offer", "create")
	if err != nil {
		t.Fatal(err)
	}
	token := checkoutToken(t, c)
	path := "/api/checkouts/" + token
	handler := f.s.CheckoutHandler()
	for _, body := range []string{`{"price_idr":1}`, `{"item_id":"other"}`, `{} {}`, `null`, `[]`, `{`} {
		w := checkoutRequest(handler, "POST", path+"/pay", "pay", body)
		if w.Code != 400 {
			t.Fatalf("body %q got %d %s", body, w.Code, w.Body.String())
		}
	}
	w := checkoutRequest(handler, "POST", path+"/pay", "", `{}`)
	if w.Code != 400 {
		t.Fatalf("missing key %d %s", w.Code, w.Body.String())
	}
	w = checkoutRequest(handler, "GET", "/api/checkouts/not-a-token", "", "")
	if w.Code != 404 {
		t.Fatalf("unknown token %d %s", w.Code, w.Body.String())
	}
	f.clock.Store(c.ExpiresAt.UnixMilli())
	w = checkoutRequest(handler, "POST", path+"/pay", "late", `{}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "CHECKOUT_EXPIRED") {
		t.Fatalf("expired pay %d %s", w.Code, w.Body.String())
	}
	if state := checkoutState(t, f.db, "lead"); state != "active" {
		t.Fatal("HTTP late pay rolled back expiry")
	}
}
func TestCheckoutHTTPHidesDatabaseFailure(t *testing.T) {
	f := checkoutSetup(t)
	handler := f.s.CheckoutHandler()
	f.db.Close()
	w := checkoutRequest(handler, "GET", "/api/checkouts/"+strings.Repeat("a", 43), "", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "database is closed") {
		t.Fatalf("unsafe failure %d %s", w.Code, w.Body.String())
	}
}
