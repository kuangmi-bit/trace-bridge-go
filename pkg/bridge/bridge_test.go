package bridge

import (
	"strings"
	"testing"
)

// Known test traceparent for deterministic assertions.
// trace-id: 0af7651916cd43dd8448eb211c80319c
// span-id:  b7ad6b7169203331
// flags:    01 (sampled)
const testTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

// BR-1: A2A → MCP propagation
// GIVEN: A2A message with metadata.traceparent
// WHEN:  Bridge to MCP tools/call
// THEN:  trace-id preserved, span-id regenerated, tracestate has bridge marker
func TestBR1_A2AToMCPPropagation(t *testing.T) {
	a2aMeta := map[string]any{
		ExtensionURI: map[string]any{
			"traceparent": testTraceparent,
			"tracestate":  "congo=t61rcWkgMzE",
			"baggage":     "agent_id=orchestrator-01",
		},
	}

	result, err := BridgeA2AToMCP(a2aMeta)
	if err != nil {
		t.Fatalf("BridgeA2AToMCP failed: %v", err)
	}

	tp, ok := result["traceparent"].(string)
	if !ok {
		t.Fatal("traceparent missing in result")
	}

	// Extract components using ParseTraceparent
	traceID, spanID, flags, err := ParseTraceparent(tp)
	if err != nil {
		t.Fatalf("ParseTraceparent failed: %v", err)
	}

	// Trace-ID MUST be preserved
	if traceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace-id changed: got %s, want 0af7651916cd43dd8448eb211c80319c", traceID)
	}

	// Span-ID MUST be regenerated (different from input)
	if spanID == "b7ad6b7169203331" {
		t.Errorf("span-id not regenerated: still %s", spanID)
	}

	// Span-ID must be 16 valid hex chars
	if len(spanID) != 16 {
		t.Errorf("span-id has wrong length: %d (expected 16)", len(spanID))
	}

	// Flags should be preserved
	if flags != "01" {
		t.Errorf("flags changed: got %s, want 01", flags)
	}

	// Tracestate MUST contain bridge marker
	ts, ok := result["tracestate"].(string)
	if !ok || !strings.Contains(ts, "deeparchi=br:1") {
		t.Errorf("tracestate missing bridge marker: got %q", ts)
	}

	// Baggage SHOULD be preserved in non-privacy mode
	bg, ok := result["baggage"].(string)
	if !ok || bg != "agent_id=orchestrator-01" {
		t.Errorf("baggage not preserved: got %q", bg)
	}
}

// BR-2: MCP → A2A propagation
// GIVEN: MCP tools/call with _meta.traceparent
// WHEN:  Bridge to A2A message
// THEN:  trace-id preserved, span-id regenerated
func TestBR2_MCPToA2APropagation(t *testing.T) {
	mcpMeta := map[string]any{
		"traceparent": testTraceparent,
		"tracestate":  "vendor=test",
		"baggage":     "session_id=abc123",
	}

	result, err := BridgeMCPToA2A(mcpMeta)
	if err != nil {
		t.Fatalf("BridgeMCPToA2A failed: %v", err)
	}

	inner, ok := result[ExtensionURI].(map[string]any)
	if !ok {
		t.Fatalf("trace context not nested under %s: %#v", ExtensionURI, result)
	}

	tp, ok := inner["traceparent"].(string)
	if !ok {
		t.Fatal("traceparent missing in result")
	}

	traceID, spanID, _, err := ParseTraceparent(tp)
	if err != nil {
		t.Fatalf("ParseTraceparent failed: %v", err)
	}

	if traceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace-id changed: got %s", traceID)
	}

	if spanID == "b7ad6b7169203331" {
		t.Errorf("span-id not regenerated")
	}

	ts := inner["tracestate"].(string)
	if !strings.Contains(ts, "deeparchi=br:1") {
		t.Errorf("tracestate missing bridge marker: %q", ts)
	}
}

// BR-3: Bridge chaining (A2A → MCP → A2A)
// GIVEN: A2A message → MCP → A2A message
// THEN:  All three share same trace-id, each generates new span-id,
//        tracestate has bridge marker exactly once
func TestBR3_BridgeChaining(t *testing.T) {
	// Step 1: A2A → MCP
	a2aMeta := map[string]any{
		ExtensionURI: map[string]any{
			"traceparent": testTraceparent,
			"tracestate":  "",
		},
	}

	mcpMeta, err := BridgeA2AToMCP(a2aMeta)
	if err != nil {
		t.Fatalf("Step 1 (A2A→MCP) failed: %v", err)
	}

	tp1 := mcpMeta["traceparent"].(string)
	traceID1, spanID1, _, _ := ParseTraceparent(tp1)

	// Step 2: MCP → A2A (using the output of step 1 as input)
	mcpInput := map[string]any{
		"traceparent": tp1,
		"tracestate":  mcpMeta["tracestate"].(string),
	}

	a2aResult, err := BridgeMCPToA2A(mcpInput)
	if err != nil {
		t.Fatalf("Step 2 (MCP→A2A) failed: %v", err)
	}

	a2aInner, ok := a2aResult[ExtensionURI].(map[string]any)
	if !ok {
		t.Fatalf("Step 2 (MCP→A2A) did not nest context under %s: %#v", ExtensionURI, a2aResult)
	}
	tp2 := a2aInner["traceparent"].(string)
	traceID2, spanID2, _, _ := ParseTraceparent(tp2)

	// Step 3: A2A → MCP again (using output of step 2)
	mcpInput2 := a2aResult

	mcpResult, err := BridgeA2AToMCP(mcpInput2)
	if err != nil {
		t.Fatalf("Step 3 (A2A→MCP again) failed: %v", err)
	}

	tp3 := mcpResult["traceparent"].(string)
	traceID3, spanID3, _, _ := ParseTraceparent(tp3)

	// All three share same trace-id
	if traceID1 != traceID2 || traceID2 != traceID3 {
		t.Errorf("trace-id diverged across chain: %s → %s → %s", traceID1, traceID2, traceID3)
	}

	// Each boundary generates new span-id (all three different)
	if spanID1 == "b7ad6b7169203331" {
		t.Errorf("step 1 span-id not regenerated")
	}
	if spanID2 == spanID1 {
		t.Errorf("step 2 span-id not regenerated (same as step 1: %s)", spanID1)
	}
	if spanID3 == spanID1 || spanID3 == spanID2 {
		t.Errorf("step 3 span-id not regenerated")
	}

	// Bridge marker appears exactly once (AppendTracestate is idempotent)
	ts := mcpResult["tracestate"].(string)
	count := strings.Count(ts, "deeparchi=br:1")
	if count != 1 {
		t.Errorf("bridge marker appears %d times in tracestate (expected 1): %q", count, ts)
	}
}

// BR-4: Privacy mode
// GIVEN: A2A message with baggage = "user_id=123"
// WHEN:  PP flag set (PrivacyMode = true)
// THEN:  Bridge strips baggage, regenerates span-id
func TestBR4_PrivacyMode(t *testing.T) {
	tc := &TraceContext{
		Traceparent: testTraceparent,
		Tracestate:  "congo=test",
		Baggage:     "user_id=123,secret_key=abc",
		PrivacyMode: true,
	}

	newSpanID, err := NewSpanID()
	if err != nil {
		t.Fatalf("NewSpanID failed: %v", err)
	}

	result, err := tc.ToMCP(newSpanID)
	if err != nil {
		t.Fatalf("ToMCP failed: %v", err)
	}

	// Baggage MUST be absent in privacy mode
	if _, exists := result["baggage"]; exists {
		t.Error("baggage should be absent in privacy mode but is present")
	}

	// traceparent must have new span-id
	tp := result["traceparent"].(string)
	_, spanID, _, _ := ParseTraceparent(tp)
	if spanID != newSpanID {
		t.Errorf("span-id not replaced: got %s, want %s", spanID, newSpanID)
	}
}

// Test ParseTraceparent with valid input
func TestParseTraceparent_Valid(t *testing.T) {
	traceID, spanID, flags, err := ParseTraceparent(testTraceparent)
	if err != nil {
		t.Fatalf("ParseTraceparent failed on valid input: %v", err)
	}
	if traceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("traceID: got %s", traceID)
	}
	if spanID != "b7ad6b7169203331" {
		t.Errorf("spanID: got %s", spanID)
	}
	if flags != "01" {
		t.Errorf("flags: got %s", flags)
	}
}

// Test ParseTraceparent with invalid inputs
func TestParseTraceparent_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"too few parts", "00-aaa-bbb"},
		{"too many parts", "00-aaa-bbb-cc-d"},
		{"wrong version", "ff-aaaabbbbccccddddeeeeffff00001111-1111222233334444-01"},
		{"short trace-id", "00-aaa-b7ad6b7169203331-01"},
		{"short span-id", "00-0af7651916cd43dd8448eb211c80319c-b7ad-01"},
		{"short flags", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := ParseTraceparent(tt.input)
			if err == nil {
				t.Errorf("expected error for input %q, got nil", tt.input)
			}
		})
	}
}

// Test NewSpanID generates 16-char hex strings
func TestNewSpanID_Format(t *testing.T) {
	sid, err := NewSpanID()
	if err != nil {
		t.Fatalf("NewSpanID failed: %v", err)
	}
	if len(sid) != 16 {
		t.Errorf("span-id length: %d (expected 16)", len(sid))
	}
	// All chars must be hex
	for _, c := range sid {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("span-id contains non-hex character: %c", c)
			break
		}
	}
}

// Test NewSpanID generates unique values
func TestNewSpanID_Uniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		sid, err := NewSpanID()
		if err != nil {
			t.Fatalf("NewSpanID failed on iteration %d: %v", i, err)
		}
		if seen[sid] {
			t.Errorf("duplicate span-id generated: %s (iteration %d)", sid, i)
			break
		}
		seen[sid] = true
	}
}

// Test AppendTracestate idempotency
func TestAppendTracestate_Idempotent(t *testing.T) {
	base := "congo=t61rcWkgMzE"
	result := AppendTracestate(base, BridgeTracestateEntry)
	if !strings.Contains(result, BridgeTracestateEntry) {
		t.Errorf("entry not added: %q", result)
	}

	// Second append should not duplicate
	result2 := AppendTracestate(result, BridgeTracestateEntry)
	if strings.Count(result2, BridgeTracestateEntry) != 1 {
		t.Errorf("entry duplicated: %q", result2)
	}

	// W3C §3.5: new entries go to the left of the list
	if !strings.HasPrefix(result, BridgeTracestateEntry+",") {
		t.Errorf("entry not leftmost (W3C §3.5): %q", result)
	}
	if !strings.HasSuffix(result, base) {
		t.Errorf("existing entries must be preserved in order: %q", result)
	}
}

// Test UpdateSpanID preserves trace-id and flags
func TestUpdateSpanID_Preserves(t *testing.T) {
	newSID := "1111222233334444"
	tp, err := UpdateSpanID(testTraceparent, newSID)
	if err != nil {
		t.Fatalf("UpdateSpanID failed: %v", err)
	}
	traceID, spanID, flags, _ := ParseTraceparent(tp)
	if traceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace-id changed")
	}
	if spanID != newSID {
		t.Errorf("span-id not updated: got %s, want %s", spanID, newSID)
	}
	if flags != "01" {
		t.Errorf("flags changed: got %s", flags)
	}
}

// Test IsBridged detection
func TestIsBridged(t *testing.T) {
	if IsBridged("") {
		t.Error("empty tracestate should not be bridged")
	}
	if IsBridged("vendor=test") {
		t.Error("unbridged tracestate should not be bridged")
	}
	if !IsBridged("deeparchi=br:1") {
		t.Error("bridge tracestate should be bridged")
	}
	if !IsBridged("vendor=test,deeparchi=br:1") {
		t.Error("mixed tracestate with bridge should be bridged")
	}
}

// Test missing trace context in A2A metadata
func Test_NoTraceContext_A2A(t *testing.T) {
	emptyMeta := map[string]any{
		"other_key": "value",
	}
	_, err := BridgeA2AToMCP(emptyMeta)
	if err == nil {
		t.Error("expected error for missing trace context, got nil")
	}
}

// Test missing trace context in MCP _meta
func Test_NoTraceContext_MCP(t *testing.T) {
	emptyMeta := map[string]any{
		"other_key": "value",
	}
	_, err := BridgeMCPToA2A(emptyMeta)
	if err == nil {
		t.Error("expected error for missing trace context, got nil")
	}
}

// BR-14: MCP → A2A output is readable as A2A metadata
// GIVEN: MCP _meta with traceparent
// WHEN:  Bridged to A2A and then read back with NewTraceContextFromA2A
// THEN:  round-trip succeeds and trace-id is preserved
// Regression: BridgeMCPToA2A used to return a flat map, so its own output
// could not be read back and did not match Message.metadata's expected shape.
func TestBR14_MCPToA2ARoundTrip(t *testing.T) {
	mcpMeta := map[string]any{"traceparent": testTraceparent}

	a2aMeta, err := BridgeMCPToA2A(mcpMeta)
	if err != nil {
		t.Fatalf("BridgeMCPToA2A failed: %v", err)
	}

	tc, err := NewTraceContextFromA2A(a2aMeta)
	if err != nil {
		t.Fatalf("output not readable as A2A metadata: %v", err)
	}

	traceID, _, _, err := ParseTraceparent(tc.Traceparent)
	if err != nil {
		t.Fatalf("ParseTraceparent failed: %v", err)
	}
	if traceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace-id changed in round trip: got %s", traceID)
	}
}
