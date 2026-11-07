package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
)

const demoTestOrigin = "http://127.0.0.1:8080"

func TestDemoHTTPPurchaseAndReset(t *testing.T) {
	app, err := NewDemoApp(t.Context(), DemoConfig{
		CheckoutOrigin: demoTestOrigin,
		ResetToken:     "test-reset",
		Model:          scriptedDemoModel{},
		Instructions:   "Use the demo's approved English templates.",
		Classify: func(_ context.Context, in agentruntime.TurnInput) (agentruntime.ScopeDecision, error) {
			if in.History[len(in.History)-1].Content == "Solve a coding problem." {
				return agentruntime.ScopeOffTopic, nil
			}
			return agentruntime.ScopeInScope, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	send := func(method, path, body string, status int, headers map[string]string) string {
		t.Helper()
		w := demoRequest(app, method, path, body, headers)
		if w.Code != status {
			t.Fatalf("%s %s: status = %d, want %d; %s", method, path, w.Code, status, w.Body)
		}
		return w.Body.String()
	}
	const thread = "/api/mock/threads/laptop-thread"
	const inventory = "/api/inventory"
	seller := map[string]string{"X-Seller-ID": "seller_demo"}
	discover := func() string { return send("POST", "/api/matches/search", `{"sellerId":"seller_demo"}`, 200, nil) }
	post := func(id, body string) {
		send("POST", "/api/mock/posts", fmt.Sprintf(`{"platform":"mock","external_post_id":%q,"buyer_id":"buyer","thread_id":%q,"text":%q,"created_at":"2026-11-07T05:00:00Z","location":{"city":"Jakarta"}}`, id, id+"-thread", body), 201, map[string]string{"Idempotency-Key": id})
	}
	stock := func(want int) {
		t.Helper()
		raw := send("GET", inventory, "", 200, seller)
		var result struct {
			Data struct {
				Items []struct {
					ID            string `json:"id"`
					PhysicalStock int    `json:"physical_stock"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		for _, item := range result.Data.Items {
			if item.ID == "item_thinkpad" {
				if item.PhysicalStock != want {
					t.Fatalf("ThinkPad physical stock = %d, want %d", item.PhysicalStock, want)
				}
				return
			}
		}
		t.Fatal("missing seeded ThinkPad")
	}
	reset := func() {
		send("POST", "/api/demo/reset", "", 204, map[string]string{"Authorization": "Bearer test-reset"})
	}
	reply := func(id, target, body string, status int) {
		send("POST", thread+"/replies", fmt.Sprintf(`{"external_reply_id":%q,"reply_to_message_id":%q,"text":%q}`, id, target, body), status, nil)
	}
	discoverLead := func() string {
		t.Helper()
		var result struct {
			Matches []struct {
				LeadID string `json:"leadId"`
			} `json:"matches"`
		}
		if err := json.Unmarshal([]byte(discover()), &result); err != nil || len(result.Matches) != 1 {
			t.Fatalf("discovery: %+v, %v", result, err)
		}
		return "/api/leads/" + result.Matches[0].LeadID
	}
	stock(2)
	send("POST", "/api/demo/reset", "", 401, nil)
	for run := range 2 {
		post("laptop", "Looking for a used laptop with at least 16 GB RAM in Jakarta.")
		send("GET", thread, "", 200, nil)
		leadPath := discoverLead()
		discover()
		messages := awaitDemoThread(t, app, thread, 1)
		offer := messages[0]
		if offer.Text != "I can offer this item for Rp 5400000." {
			t.Fatalf("initial offer = %q", offer.Text)
		}
		if run == 0 {
			reply("scope", offer.ID, "Solve a coding problem.", 201)
			messages = awaitDemoThread(t, app, thread, 3)
			if messages[2].Text != marketplace.AgentOffTopicRedirectText {
				t.Fatalf("off-topic redirect = %q", messages[2].Text)
			}
			if got := send("GET", leadPath, "", 200, seller); !strings.Contains(got, `"state":"active"`) {
				t.Fatal("off-topic request changed lead state")
			}
		}
		reply("accept", offer.ID, "I accept. Please send the checkout link.", 201)
		reply("accept", offer.ID, "I accept. Please send the checkout link.", 200)
		messages = awaitDemoThread(t, app, thread, len(messages)+2)
		_, token, found := strings.Cut(messages[len(messages)-1].Text, demoTestOrigin+"/checkout/")
		if !found || token == "" {
			t.Fatal("checkout was not published")
		}
		checkout := "/api/checkouts/" + token
		send("GET", checkout, "", 200, nil)
		first := send("POST", checkout+"/pay", "{}", 200, map[string]string{"Idempotency-Key": "pay"})
		second := send("POST", checkout+"/pay", "{}", 200, map[string]string{"Idempotency-Key": "repeat"})
		if first != second {
			t.Fatal("payment replay changed result")
		}
		stock(1)
		if got := send("GET", leadPath, "", 200, seller); !strings.Contains(got, `"state":"paid"`) || strings.Contains(got, token) {
			t.Fatal("seller projection must show paid state and omit the checkout token")
		}
		reset()
		send("GET", thread, "", 404, nil)
		send("GET", checkout, "", 404, nil)
		send("GET", leadPath, "", 404, seller)
		stock(2)
	}
	post("sale", "For sale: used laptop with 16 GB RAM in Jakarta.")
	if raw := discover(); !strings.Contains(raw, `"matches":[]`) {
		t.Fatal("selling post produced a lead")
	}
	reset()
	post("laptop", "Looking for a used laptop in Jakarta.")
	leadPath := discoverLead()
	offer := awaitDemoThread(t, app, thread, 1)[0]
	reply("below-floor", offer.ID, "I can only pay Rp 5.1 million.", 201)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := send("GET", leadPath, "", 200, seller); strings.Contains(got, `"state":"escalated"`) {
			if !strings.Contains(got, `"price_idr":5400000`) || strings.Contains(got, "5100000") {
				t.Fatal("below-floor request changed offer terms")
			}
			messages := awaitDemoThread(t, app, thread, 2)
			if messages[0] != offer || messages[1].Text != "I can only pay Rp 5.1 million." {
				t.Fatal("escalation must not publish another seller message")
			}
			stock(2)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("below-floor turn did not escalate after rejection")
}

type demoTestMessage struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func awaitDemoThread(t *testing.T, h http.Handler, path string, count int) []demoTestMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w := demoRequest(h, "GET", path, "", nil)
		var thread struct {
			Messages []demoTestMessage `json:"messages"`
		}
		if w.Code != 200 {
			t.Fatalf("thread status = %d", w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &thread); err != nil {
			t.Fatal(err)
		}
		if len(thread.Messages) >= count {
			if len(thread.Messages) != count {
				t.Fatalf("thread contains %d messages, want %d", len(thread.Messages), count)
			}
			return thread.Messages
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("thread did not reach %d messages", count)
	return nil
}

func demoRequest(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type scriptedDemoModel struct{}

func (scriptedDemoModel) GenerateJSON(_ context.Context, req llm.JSONRequest, out any) error {
	if req.SchemaName == "inventory_queries" {
		return json.Unmarshal([]byte(`{"queries":[{"query":"laptop","item_ids":["item_thinkpad"]}]}`), out)
	}
	buying := !strings.Contains(req.Input, "For sale:")
	matches := `[]`
	if buying {
		matches = `[{"item_id":"item_thinkpad","score":0.95,"reason":"Laptop with verified RAM"}]`
	}
	return json.Unmarshal([]byte(fmt.Sprintf(`{"is_buying_request":%t,"confidence":0.99,"item_query":"used laptop","quantity":1,"requirements":[{"name":"ram_gb_min","value":"16"}],"city":"Jakarta","budget_amount":6000000,"budget_currency":"IDR","urgency_text":null,"needed_by":null,"matches":%s}`, buying, matches)), out)
}

func (scriptedDemoModel) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	accepting, belowFloor := false, false
	for _, message := range req.Messages {
		accepting = accepting || message.Content == "I accept. Please send the checkout link."
		belowFloor = belowFloor || message.Content == "I can only pay Rp 5.1 million."
	}
	name, args := "make_offer", `{"item_id":"item_thinkpad","price":5400000}`
	if accepting {
		name, args = "create_checkout", `{}`
	}
	if belowFloor {
		name, args = "make_offer", `{"item_id":"item_thinkpad","price":5100000}`
		return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{{ID: "below_floor", Name: name, Args: json.RawMessage(args)}}}, nil
	}
	if len(req.ToolResults) > 0 && req.ToolResults[0].CallID == "below_floor" {
		if !strings.Contains(req.ToolResults[0].Output, "PRICE_BELOW_MINIMUM") {
			return llm.Response{}, fmt.Errorf("scripted offer did not return below-floor rejection")
		}
		name, args = "escalate_to_seller", `{"reason":"Buyer budget is below allowed terms."}`
	} else if len(req.ToolResults) > 0 {
		name = "send_message"
		template := marketplace.AgentOfferText
		if req.ToolResults[0].CallID == "create_checkout" {
			template = marketplace.AgentCheckoutText
		}
		args = fmt.Sprintf(`{"text":%q}`, template)
	}
	return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{{ID: name, Name: name, Args: json.RawMessage(args)}}}, nil
}

func TestDemoIntakeRefreshesExpiryBeforeCachingResult(t *testing.T) {
	expired := false
	attempts := 0
	intake := newDemoIntake(demoIntake(func(context.Context, contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
		attempts++
		if !expired {
			return "", agentruntime.ErrLeadNotActive
		}
		return contracts.BuyerReplyAcceptedVolatile, nil
	}), func(context.Context) (int, error) {
		expired = true
		return 1, nil
	})
	for range 2 {
		ack, err := intake.AcceptBuyerReply(t.Context(), contracts.BuyerReplyEvent{ID: "reply-after-expiry"})
		if err != nil || ack != contracts.BuyerReplyAcceptedVolatile {
			t.Fatalf("reply after expiry = %q, %v", ack, err)
		}
	}
	if attempts != 1 {
		t.Fatalf("intake accepted the same event %d times", attempts)
	}
}
