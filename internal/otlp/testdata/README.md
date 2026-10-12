# Codex OTLP fixtures

`codex_0_145_skill_tbt.pb.b64` is the existing Codex 0.145.0 Skill/TBT compatibility sample.

`codex_0_162_1_logs.pb.b64` and `codex_0_162_1_metrics.pb.b64` were captured on 2026-10-12 from the official macOS arm64 Codex 0.162.1 binary (source commit `092d3acd6bec3e3a14bdc7e7a2810ab628ab759d`). These are real OTLP/gRPC Export requests, serialized by Codex's exporter and captured by this monitor, then sanitized and re-marshaled.

The app server used a local Responses API stub with two responses per turn (input 100, output 20, cached input 20, cache write 30, reasoning 5). A local tool printed harmless fixture text large enough to trigger an output preview; an explicitly selected local `fixture` skill produced a real invocation and injection. A local turn-cost endpoint returned `1.250001`; the app server remained alive past its 150-second polling interval for that event. Metrics capture enabled analytics and DELTA temporality. Two sessions are included, one without metrics and one with metrics, so the combined log fixture intentionally has more responses than the metric fixture.

Sanitization retained only allowlisted protocol fields, replaced conversation/turn/call IDs, removed identity attributes, paths, bodies and trace identifiers, and replaced tool arguments/output with equal-length `x` strings. Original event/data-point types, timestamps, count/sum, temporality, decimal cost, TTFT and truncation flag were preserved. No hand-written OTLP messages were used to create these golden fixtures. Branch tests clone these real records and change individual attributes.

The SSE fixture includes both measured completions and unmeasured completion notifications. Only the former may contribute requests, tokens and local cost.
