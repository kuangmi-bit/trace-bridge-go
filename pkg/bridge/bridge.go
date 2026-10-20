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

	// SpecVersion is the revision of the bridge specification this package
	// tracks. It is not the wire token: the tracestate entry stays
	// `deeparchi=br:1` until the entry's meaning changes incompatibly.
	SpecVersion = "0.3.1"
)

// TraceContext holds W3C Trace Context fields.
type TraceContext struct {
	Traceparent string // "00-{trace-id}-{parent-id}-{flags}"
	Tracestate  string // "vendor=value,..."
	Baggage     string // "key=value,..."
	PrivacyMode bool   // Strip baggage, regenerate span-id at boundaries
}

// ParseTraceparent splits a traceparent header into its components.
// Returns traceID (32 hex), spanID (16 hex), flags (2 hex).
//
// Version 00 is parsed strictly: exactly 55 characters, four fields. Higher
// versions are parsed positionally over the first 55 characters, as W3C Trace
// Context §3.2.4 requires ("if a higher version is detected, the
// implementation SHOULD try to parse it"), so a future revision does not break
// the chain. Version ff is forbidden by the specification.
func ParseTraceparent(tp string) (traceID, spanID, flags string, err error) {
	if len(tp) < 55 {
		return "", "", "", fmt.Errorf("invalid traceparent: length %d is shorter than the 55-character minimum", len(tp))
	}
	if tp[2] != '-' {
		return "", "", "", fmt.Errorf("invalid traceparent: malformed version prefix")
	}
	version := tp[:2]
	if !isHex(version) {
		return "", "", "", fmt.Errorf("invalid traceparent: version is not hexadecimal: %q", version)
	}
	if version == "ff" {
		return "", "", "", fmt.Errorf("invalid traceparent: version ff is forbidden")
	}
	if version == "00" && len(tp) != 55 {
		return "", "", "", fmt.Errorf("invalid traceparent: version 00 must be exactly 55 characters, got %d", len(tp))
	}
	if tp[35] != '-' || tp[52] != '-' {
		return "", "", "", fmt.Errorf("invalid traceparent: malformed field delimiters")
	}

	traceID, spanID, flags = tp[3:35], tp[36:52], tp[53:55]
	if !isHex(traceID) || !isHex(spanID) || !isHex(flags) {
		return "", "", "", fmt.Errorf("invalid traceparent: non-hexadecimal field")
	}
	if traceID == "00000000000000000000000000000000" {
		return "", "", "", fmt.Errorf("invalid traceparent: all-zero trace-id")
	}
	if spanID == "0000000000000000" {
		return "", "", "", fmt.Errorf("invalid traceparent: all-zero parent-id")
	}
	return traceID, spanID, flags, nil
}

// isHex reports whether s is a non-empty lowercase-or-uppercase hexadecimal string.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
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
// The version field is preserved. Any trailing fields introduced by a higher
// version are not carried over; the result is always a four-field value.
func UpdateSpanID(traceparent string, newSpanID string) (string, error) {
	if len(newSpanID) != 16 || !isHex(newSpanID) || newSpanID == "0000000000000000" {
		return "", fmt.Errorf("invalid span-id: %q (expected 16 hexadecimal characters, not all zero)", newSpanID)
	}
	traceID, _, flags, err := ParseTraceparent(traceparent)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s-%s", traceparent[:2], traceID, newSpanID, flags), nil
}

// AppendTracestate adds an entry to a tracestate string if not already present.
// W3C Trace Context §3.5 places a newly added or modified entry at the beginning
// (left) of the list, where size truncation reaches last; the bridge entry
// therefore goes first rather than last, despite the historical function name.
func AppendTracestate(tracestate, entry string) string {
	if tracestate == "" {
		return entry
	}
	if strings.Contains(tracestate, entry) {
		return tracestate // Already present; bridge is idempotent
	}
	return entry + "," + tracestate
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
// Extracts trace context from MCP _meta, generates a new span, and returns an
// A2A metadata map with the context nested under ExtensionURI, ready to assign
// to Message.metadata.
func BridgeMCPToA2A(meta map[string]any) (map[string]any, error) {
	tc, err := NewTraceContextFromMCP(meta)
	if err != nil {
		return nil, err
	}

	newSpanID, err := NewSpanID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate span ID: %w", err)
	}

	inner, err := tc.ToA2A(newSpanID)
	if err != nil {
		return nil, err
	}

	return map[string]any{ExtensionURI: inner}, nil
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
