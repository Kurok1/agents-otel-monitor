/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

package store

// Optional token counts need their own evidence counts: old records that did
// not report a field must remain unknown in response/turn comparisons.
const archiveCodexResponseSamplesSQL = `
INSERT INTO archive.usage_hourly (bucket_start, client, model, cached_input_samples, cache_write_input_samples, reasoning_output_samples)
SELECT date_trunc('hour',ts), 'codex', COALESCE(model,''), COUNT(cached_token_count), COUNT(cache_write_token_count), COUNT(reasoning_token_count)
FROM codex_valid_token_usage WHERE ts>=? AND ts<? GROUP BY 1,2,3
ON CONFLICT (bucket_start, client, model) DO UPDATE SET
 cached_input_samples = archive.usage_hourly.cached_input_samples + excluded.cached_input_samples,
 cache_write_input_samples = archive.usage_hourly.cache_write_input_samples + excluded.cache_write_input_samples,
 reasoning_output_samples = archive.usage_hourly.reasoning_output_samples + excluded.reasoning_output_samples`

// Keep both cost sources, resolving the conversation's source at query time.
// This makes a late native turn correct every affected window automatically.
const archiveCodexLocalCostSQL = `
INSERT INTO archive.codex_local_cost_hourly
SELECT date_trunc('hour', ts), COALESCE(conversation_id, ''), COALESCE(model, ''), SUM(cost_usd), COUNT(cost_usd)
FROM codex_valid_token_usage WHERE ts >= ? AND ts < ? GROUP BY 1, 2, 3
ON CONFLICT (bucket_start, conversation_id, model) DO UPDATE SET
    cost_usd = CASE WHEN archive.codex_local_cost_hourly.cost_usd IS NULL AND excluded.cost_usd IS NULL THEN NULL
                   ELSE COALESCE(archive.codex_local_cost_hourly.cost_usd, 0) + COALESCE(excluded.cost_usd, 0) END,
    sample_count = archive.codex_local_cost_hourly.sample_count + excluded.sample_count`

const archiveCodexTurnCostSQL = `
INSERT INTO archive.codex_turn_cost
SELECT conversation_id, turn_id, ts, received_at, model, estimated_usd
FROM codex_event_turn_cost WHERE ts >= ? AND ts < ?
QUALIFY row_number() OVER (PARTITION BY conversation_id, turn_id ORDER BY ts DESC, received_at DESC, CAST(estimated_usd AS DECIMAL(38,18)) DESC) = 1
ON CONFLICT (conversation_id, turn_id) DO UPDATE SET
    ts = excluded.ts, received_at = excluded.received_at,
    model = excluded.model, estimated_usd = excluded.estimated_usd
WHERE excluded.ts > archive.codex_turn_cost.ts
   OR (excluded.ts = archive.codex_turn_cost.ts AND excluded.received_at > archive.codex_turn_cost.received_at)
   OR (excluded.ts = archive.codex_turn_cost.ts AND excluded.received_at = archive.codex_turn_cost.received_at
       AND CAST(excluded.estimated_usd AS DECIMAL(38,18)) > CAST(archive.codex_turn_cost.estimated_usd AS DECIMAL(38,18)))`

const archiveCodexSkillsSQL = `
INSERT INTO archive.codex_skill_invocation
SELECT conversation_id, turn_id, skill_name, COALESCE(plugin_id, ''), invocation_type, MIN(ts), MIN(scope)
FROM codex_event_skill_invocation WHERE ts >= ? AND ts < ? GROUP BY 1, 2, 3, 4, 5
ON CONFLICT (conversation_id, turn_id, skill_name, plugin_id, invocation_type) DO UPDATE SET
    ts = LEAST(archive.codex_skill_invocation.ts, excluded.ts),
    scope = COALESCE(archive.codex_skill_invocation.scope, excluded.scope)`

const archiveCodexTTFTSQL = `
INSERT INTO archive.codex_ttft_histogram
SELECT date_trunc('hour', ts), COALESCE(conversation_id, ''), COALESCE(model, ''), ttft_ms, COUNT(*)
FROM codex_valid_token_usage WHERE ts >= ? AND ts < ? AND ttft_ms >= 0 GROUP BY 1, 2, 3, 4
ON CONFLICT (bucket_start, conversation_id, model, ttft_ms) DO UPDATE SET
    sample_count = archive.codex_ttft_histogram.sample_count + excluded.sample_count`

const archiveCodexTurnUsageSQL = `
INSERT INTO archive.codex_turn_usage_hourly
SELECT date_trunc('hour', ts), COALESCE(model, ''), token_type, COALESCE(session_source, ''), SUM(sample_count), SUM(sum_tokens)
FROM codex_metric_turn_token_usage WHERE ts >= ? AND ts < ? GROUP BY 1, 2, 3, 4
ON CONFLICT (bucket_start, model, token_type, session_source) DO UPDATE SET
    sample_count = archive.codex_turn_usage_hourly.sample_count + excluded.sample_count,
    sum_tokens = archive.codex_turn_usage_hourly.sum_tokens + excluded.sum_tokens`

const archiveCodexSessionCacheSQL = `
INSERT INTO archive.sessions (client, session_id, cache_write_tokens)
SELECT 'codex', conversation_id, SUM(COALESCE(cache_write_token_count, 0))
FROM codex_valid_token_usage WHERE ts >= ? AND ts < ? AND conversation_id IS NOT NULL GROUP BY 1, 2
ON CONFLICT (client, session_id) DO UPDATE SET
    cache_write_tokens = COALESCE(archive.sessions.cache_write_tokens, 0) + excluded.cache_write_tokens`

// Activity is retained even when a completion has no valid usage, or a skill
// or cost event is the only signal received for a conversation.
const archiveCodexActivitySQL = `
INSERT INTO archive.sessions (client, session_id, first_active, last_active)
SELECT 'codex', conversation_id, MIN(ts), MAX(ts) FROM (
    SELECT conversation_id, ts FROM codex_event_token_usage WHERE ts >= ? AND ts < ?
    UNION ALL SELECT conversation_id, ts FROM codex_event_skill_invocation WHERE ts >= ? AND ts < ?
    UNION ALL SELECT conversation_id, ts FROM codex_event_turn_cost WHERE ts >= ? AND ts < ?
) activity WHERE conversation_id IS NOT NULL GROUP BY 1, 2
ON CONFLICT (client, session_id) DO UPDATE SET
    first_active = LEAST(archive.sessions.first_active, excluded.first_active),
    last_active = GREATEST(archive.sessions.last_active, excluded.last_active)`
