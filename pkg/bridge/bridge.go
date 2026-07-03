// Package bridge implements the Cross-Protocol Trace Context Bridge v0.1.
//
// Provides bidirectional mapping of W3C Trace Context between
// A2A metadata[extension-uri] and MCP params._meta trace fields.
package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// ExtensionURI is the A2A extension URI for trace context propagation.
	ExtensionURI = "https://deeparchi.ai/extensions/trace-context/v1"

	// BridgeTracestateEntry identifies bridge-propagated traces.
	BridgeTracestateEntry = "deeparchi=br:1"

	// SpecVersion is the bridge specification version.
	SpecVersion = "0.1"
)

// TraceContext holds W3C Trace Context fields.
type TraceContext struct {
	Traceparent string            // "00-{trace-id}-{parent-id}-{flags}"
	Tracestate  string            // "vendor=value,..."
	Baggage     string            // "key=value,..."
	PrivacyMode bool              // Strip baggage, regenerate span-id at boundaries
}

// ParseTraceparent splits a traceparent header into its components.
// Returns traceID (32 hex), spanID (16 hex), flags (2 hex).
func ParseTraceparent(tp string) (traceID, spanID, flags string, err error) {
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		return "", "", "", fmt.Errorf("invalid traceparent: expected 4 parts, got %d", len(parts))
	}
	if parts[0] != "00" {
		return "", "", "", fmt.Errorf("unsupported traceparent version: %s (expected 00)", parts[0])
	}
	if len(parts[1]) != 32 {
		return "", "", "", fmt.Errorf("invalid trace-id length: %d (expected 32)", len(parts[1]))
	}
	if len(parts[2]) != 16 {
		return "", "", "", fmt.Errorf("invalid parent-id length: %d (expected 16)", len(parts[2]))
	}
	if len(parts[3]) != 2 {
		return "", "", "", fmt.Errorf("invalid trace-flags length: %d (expected 2)", len(parts[3]))
	}
	return parts[1], parts[2], parts[3], nil
}

// NewSpanID generates a cryptographically random 16-char hex span ID.
func NewSpanID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate span ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// UpdateSpanID replaces the span-id portion of a traceparent.
func UpdateSpanID(traceparent string, newSpanID string) (string, error) {
	traceID, _, flags, err := ParseTraceparent(traceparent)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("00-%s-%s-%s", traceID, newSpanID, flags), nil
}

// AppendTracestate adds an entry to a tracestate string if not already present.
func AppendTracestate(tracestate, entry string) string {
	if tracestate == "" {
		return entry
	}
	if strings.Contains(tracestate, entry) {
		return tracestate // Already present; bridge is idempotent
	}
	return tracestate + "," + entry
}

// IsBridged checks if a tracestate indicates prior bridge propagation.
func IsBridged(tracestate string) bool {
	return strings.Contains(tracestate, BridgeTracestateEntry)
}

// PrivacyStripped removes baggage and regenerates span-id for privacy-preserving propagation.
type PrivacyStripped struct {
	TraceContext
	OriginalSpanID string // Preserved for audit log only
}

// NewTraceContextFromA2A extracts trace context from A2A message metadata.
// The metadata map should have the ExtensionURI key with a value that is
// a map[string]any containing "traceparent" and optionally "tracestate" and "baggage".
func NewTraceContextFromA2A(metadata map[string]any) (*TraceContext, error) {
	raw, ok := metadata[ExtensionURI]
	if !ok {
		return nil, fmt.Errorf("no trace context extension in metadata")
	}
	tcMap, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid trace context extension format")
	}

	tp, ok := tcMap["traceparent"].(string)
	if !ok {
		return nil, fmt.Errorf("missing traceparent in trace context")
	}

	tc := &TraceContext{Traceparent: tp}
	if ts, ok := tcMap["tracestate"].(string); ok {
		tc.Tracestate = ts
	}
	if bg, ok := tcMap["baggage"].(string); ok {
		tc.Baggage = bg
	}
	return tc, nil
}

// NewTraceContextFromMCP extracts trace context from MCP params._meta.
func NewTraceContextFromMCP(meta map[string]any) (*TraceContext, error) {
	tp, ok := meta["traceparent"].(string)
	if !ok {
		return nil, fmt.Errorf("missing traceparent in MCP _meta")
	}

	tc := &TraceContext{Traceparent: tp}
	if ts, ok := meta["tracestate"].(string); ok {
		tc.Tracestate = ts
	}
	if bg, ok := meta["baggage"].(string); ok {
		tc.Baggage = bg
	}
	return tc, nil
}

// ToA2A converts trace context to A2A message metadata format.
// Returns a map suitable for embedding under the ExtensionURI key in message metadata.
func (tc *TraceContext) ToA2A(newSpanID string) (map[string]any, error) {
	tp, err := UpdateSpanID(tc.Traceparent, newSpanID)
	if err != nil {
		return nil, err
	}

	ts := AppendTracestate(tc.Tracestate, BridgeTracestateEntry)

	result := map[string]any{
		"traceparent": tp,
		"tracestate":  ts,
	}
	if tc.Baggage != "" && !tc.PrivacyMode {
		result["baggage"] = tc.Baggage
	}
	return result, nil
}

// ToMCP converts trace context to MCP params._meta format.
func (tc *TraceContext) ToMCP(newSpanID string) (map[string]any, error) {
	tp, err := UpdateSpanID(tc.Traceparent, newSpanID)
	if err != nil {
		return nil, err
	}

	ts := AppendTracestate(tc.Tracestate, BridgeTracestateEntry)

	result := map[string]any{
		"traceparent": tp,
		"tracestate":  ts,
	}
	if tc.Baggage != "" && !tc.PrivacyMode {
		result["baggage"] = tc.Baggage
	}
	return result, nil
}

// BridgeA2AToMCP performs a complete A2A → MCP bridge operation.
// Extracts trace context from A2A metadata, generates new span,
// returns MCP _meta map ready for injection.
func BridgeA2AToMCP(metadata map[string]any) (map[string]any, error) {
	tc, err := NewTraceContextFromA2A(metadata)
	if err != nil {
		return nil, err // No trace context; caller should start new trace
	}

	newSpanID, err := NewSpanID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate span ID: %w", err)
	}

	return tc.ToMCP(newSpanID)
}

// BridgeMCPToA2A performs a complete MCP → A2A bridge operation.
func BridgeMCPToA2A(meta map[string]any) (map[string]any, error) {
	tc, err := NewTraceContextFromMCP(meta)
	if err != nil {
		return nil, err
	}

	newSpanID, err := NewSpanID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate span ID: %w", err)
	}

	return tc.ToA2A(newSpanID)
}

// StripBaggage returns a new TraceContext with baggage removed (privacy mode).
func (tc *TraceContext) StripBaggage() *TraceContext {
	return &TraceContext{
		Traceparent: tc.Traceparent,
		Tracestate:  tc.Tracestate,
		Baggage:     "",
		PrivacyMode: true,
	}
}
