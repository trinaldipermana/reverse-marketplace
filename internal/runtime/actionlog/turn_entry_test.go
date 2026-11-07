package actionlog

import "testing"

func TestTurnEntryLifecycleAndDeliveryStatus(t *testing.T) {
	for _, kind := range []Kind{KindContextLoaded, KindTurnStarted, KindTurnCompleted} {
		e := Entry{EventID: "mock:reply:123", LeadID: "lead", SellerID: "seller", Kind: kind, Status: StatusOK, Code: CodeOK}
		if err := e.Validate(); err != nil || e.Summary() == "" {
			t.Fatalf("kind=%s error=%v", kind, err)
		}
	}
	e := Entry{EventID: "event", LeadID: "lead", SellerID: "seller", Kind: KindTool, Tool: ToolSendMessage, ToolCallID: "call:send", Status: StatusOK, Code: CodePending}
	if err := e.Validate(); err != nil || e.Summary() == toolSummary[ToolSendMessage] {
		t.Fatalf("pending claimed delivery: %v", err)
	}
	e.Tool = ToolMakeOffer
	if e.Validate() == nil {
		t.Fatal("delivery pending accepted for unrelated tool")
	}
}
