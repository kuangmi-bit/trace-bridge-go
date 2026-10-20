# trace-bridge-go

A2A ↔ MCP W3C Trace Context bridge — Go reference implementation of the *Cross-Protocol Trace Context Bridge* specification (as of spec v0.3).

When an A2A agent invokes an MCP tool, the trace stops at the protocol boundary: the A2A message carries one trace context, the tool call starts another, and the two show up in the backend as unrelated trees. This package carries one W3C trace context across that boundary in both directions, using each protocol's own metadata extension point — no protocol extension, no side channel.

Specification: [`deeparchi-ai/macs` → `trace-bridge/spec.md`](https://github.com/deeparchi-ai/macs/blob/main/trace-bridge/spec.md).

## Mapping

| Direction | From | To |
|---|---|---|
| A2A → MCP | `message.metadata["https://deeparchi.ai/extensions/trace-context/v1"]` | `params._meta` |
| MCP → A2A | `params._meta` | `message.metadata["https://deeparchi.ai/extensions/trace-context/v1"]` |

Rules applied at every hop:

- `trace-id` is invariant; a new `span-id` per hop — taken from the host's OpenTelemetry SDK when it has one, or from `NewSpanID` (CSPRNG, 16 hex chars) when it does not.
- `tracestate` is preserved and `deeparchi=br:1` is appended once (idempotent — a second bridge over an already-bridged context does not re-wrap).
- `baggage` is forwarded only outside privacy mode; `StripBaggage` removes it while keeping the trace chain.
- Missing or malformed `traceparent` returns an error rather than propagating garbage; the caller decides whether to start a new root trace.

The MCP side needs no vendor key: MCP specification 2026-07-28 reserves `traceparent`, `tracestate` and `baggage` as unprefixed `_meta` keys and requires W3C Trace Context / W3C Baggage formats ([§ `_meta`](https://modelcontextprotocol.io/specification/2026-07-28/basic)). A2A v1.0.1 has no equivalent reserved field, so the A2A side uses the extension URI above.

## Install

```bash
go get github.com/kuangmi-bit/trace-bridge-go
```

Requires Go 1.26 or newer. No third-party dependencies.

## Usage

A2A → MCP, inside the agent that received the A2A message:

```go
mcpMeta, err := bridge.BridgeA2AToMCP(a2aMessage.Metadata)
if err != nil {
    // no usable trace context: start a new root trace for this tool call
}
// mcpMeta is {"traceparent": ..., "tracestate": ..., "baggage"?: ...}
req.Params.Meta = mcpMeta
```

MCP → A2A, inside the tool that delegates back to an agent:

```go
a2aMeta, err := bridge.BridgeMCPToA2A(req.Params.Meta)
if err != nil {
    // no usable trace context
}
msg.Metadata = a2aMeta // already nested under bridge.ExtensionURI
```

Lower-level pieces, when the caller wants to control span-id generation:

```go
traceID, parentSpanID, flags, err := bridge.ParseTraceparent(tp)
newSpanID, err := bridge.NewSpanID()
tp, err = bridge.UpdateSpanID(tp, newSpanID)
ts = bridge.AppendTracestate(ts, bridge.BridgeTracestateEntry)
bridged := bridge.IsBridged(ts)
```

## API

| Symbol | Purpose |
|---|---|
| `BridgeA2AToMCP(metadata) (map[string]any, error)` | Full A2A → MCP hop |
| `BridgeMCPToA2A(meta) (map[string]any, error)` | Full MCP → A2A hop, nested under `ExtensionURI` |
| `NewTraceContextFromA2A(metadata)` / `NewTraceContextFromMCP(meta)` | Extract into `TraceContext` |
| `(*TraceContext).ToA2A(spanID)` / `ToMCP(spanID)` | Serialise into the other side's shape |
| `ParseTraceparent`, `UpdateSpanID`, `NewSpanID` | W3C field handling |
| `AppendTracestate`, `IsBridged` | `deeparchi=br:1` bookkeeping |
| `(*TraceContext).StripBaggage()` | Privacy mode |
| `ExtensionURI`, `BridgeTracestateEntry`, `SpecVersion` | Constants |

## What this package does not do

- **It does not create spans and does not choose parents.** It moves context; span creation and parent selection belong to your OpenTelemetry instrumentation. Per spec §5.0, an outbound span's parent comes from the local operation Context, and the extracted remote Context is only a fallback when no local span exists.
- **It does not cover operations with no metadata field.** In A2A v1.0.1, `GetTask` and `SubscribeToTask` requests carry no `metadata`, so a message-level carrier cannot reach them; transport headers or a task-level trace id would be needed, and neither is implemented here.
- **It does not strip baggage on its own.** Privacy mode is opt-in via `StripBaggage`; cross-tenant deployments must call it.
- **It is not a sampling or export layer.** No OTel SDK, no exporter, no backend assumptions.

## Tests

```bash
go test ./...
```

14 test functions / 21 cases, covering both directions, multi-hop chaining, MCP→A2A round-trip readability, privacy mode, malformed input and idempotence.

## Status

Reference implementation v0.1 (2026-07-03), implementing the mapping rules of the specification as of spec v0.3. Interfaces may change; it is not production-hardened. Known deviation: `tracestate` entries are appended to the right, while W3C §3.5 recommends the left.

## License

No license file is declared in this repository yet. The specification it implements is MIT-licensed as part of `deeparchi-ai/macs`. Open an issue if you want to reuse the code.
