-- @author Kurok1 <im.kurokyhanc@gmail.com>
-- @since 3.2.0

ALTER TABLE codex_event_token_usage ADD COLUMN IF NOT EXISTS ttft_ms BIGINT;
ALTER TABLE codex_event_token_usage ADD COLUMN IF NOT EXISTS cache_write_token_count BIGINT;
ALTER TABLE codex_event_token_usage ADD COLUMN IF NOT EXISTS error VARCHAR;
ALTER TABLE codex_event_token_usage ADD COLUMN IF NOT EXISTS usage_valid BOOLEAN;
ALTER TABLE codex_event_tool_result ADD COLUMN IF NOT EXISTS output_truncated BOOLEAN;

UPDATE codex_event_token_usage SET
    ttft_ms = TRY_CAST(json_extract_string(TRY_CAST(attrs AS JSON), '$.ttft_ms') AS BIGINT),
    cache_write_token_count = TRY_CAST(json_extract_string(TRY_CAST(attrs AS JSON), '$.cache_write_token_count') AS BIGINT),
    error = json_extract_string(TRY_CAST(attrs AS JSON), '$."error.message"')
WHERE usage_valid IS NULL;
UPDATE codex_event_token_usage SET ttft_ms = NULL WHERE ttft_ms < 0;
UPDATE codex_event_tool_result SET output_truncated = TRY_CAST(json_extract_string(TRY_CAST(attrs AS JSON), '$.output_truncated') AS BOOLEAN) WHERE output_truncated IS NULL;

CREATE OR REPLACE VIEW codex_valid_token_usage AS
SELECT * FROM codex_event_token_usage
WHERE COALESCE(usage_valid, TRUE)
  AND input_token_count >= 0 AND output_token_count >= 0
  AND COALESCE(TRIM(error), '') = ''
  AND COALESCE(cached_token_count, 0) BETWEEN 0 AND input_token_count
  AND COALESCE(cache_write_token_count, 0) BETWEEN 0 AND input_token_count - COALESCE(cached_token_count, 0)
  AND COALESCE(reasoning_token_count, 0) BETWEEN 0 AND output_token_count
  AND CAST(input_token_count AS HUGEINT) + output_token_count <= 9223372036854775807;
UPDATE codex_event_token_usage SET usage_valid = COALESCE(
    input_token_count >= 0 AND output_token_count >= 0
    AND COALESCE(TRIM(error), '') = ''
    AND COALESCE(cached_token_count, 0) BETWEEN 0 AND input_token_count
    AND COALESCE(cache_write_token_count, 0) BETWEEN 0 AND input_token_count - COALESCE(cached_token_count, 0)
    AND COALESCE(reasoning_token_count, 0) BETWEEN 0 AND output_token_count
    AND CAST(input_token_count AS HUGEINT) + output_token_count <= 9223372036854775807, FALSE)
WHERE usage_valid IS NULL;

CREATE TABLE IF NOT EXISTS codex_event_skill_invocation (
    ts TIMESTAMP NOT NULL,
    received_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id VARCHAR,
    app_version VARCHAR,
    auth_mode VARCHAR,
    originator VARCHAR,
    terminal_type VARCHAR,
    model VARCHAR,
    slug VARCHAR,
    user_account_id VARCHAR,
    user_email VARCHAR,
    turn_id VARCHAR NOT NULL,
    skill_name VARCHAR NOT NULL,
    scope VARCHAR,
    plugin_id VARCHAR,
    invocation_type VARCHAR NOT NULL,
    attrs VARCHAR
);

CREATE TABLE IF NOT EXISTS codex_event_turn_cost (
    ts TIMESTAMP NOT NULL,
    received_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id VARCHAR,
    app_version VARCHAR,
    auth_mode VARCHAR,
    originator VARCHAR,
    terminal_type VARCHAR,
    model VARCHAR,
    slug VARCHAR,
    user_account_id VARCHAR,
    user_email VARCHAR,
    turn_id VARCHAR NOT NULL,
    estimated_usd VARCHAR NOT NULL,
    interrupted BOOLEAN,
    speed VARCHAR,
    reasoning_effort VARCHAR,
    attrs VARCHAR
);

CREATE TABLE IF NOT EXISTS codex_metric_turn_token_usage (
    ts TIMESTAMP NOT NULL,
    start_ts TIMESTAMP NOT NULL,
    received_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    sample_count BIGINT NOT NULL,
    sum_tokens DOUBLE NOT NULL,
    conversation_id VARCHAR,
    app_version VARCHAR,
    auth_mode VARCHAR,
    originator VARCHAR,
    terminal_type VARCHAR,
    model VARCHAR,
    slug VARCHAR,
    user_account_id VARCHAR,
    user_email VARCHAR,
    token_type VARCHAR NOT NULL,
    session_source VARCHAR,
    attrs VARCHAR
);

ALTER TABLE archive.sessions ADD COLUMN IF NOT EXISTS cache_write_tokens BIGINT DEFAULT 0;
ALTER TABLE archive.usage_hourly ADD COLUMN IF NOT EXISTS cached_input_samples BIGINT DEFAULT 0;
ALTER TABLE archive.usage_hourly ADD COLUMN IF NOT EXISTS cache_write_input_samples BIGINT DEFAULT 0;
ALTER TABLE archive.usage_hourly ADD COLUMN IF NOT EXISTS reasoning_output_samples BIGINT DEFAULT 0;

CREATE TABLE IF NOT EXISTS archive.codex_local_cost_hourly (
    bucket_start TIMESTAMP NOT NULL,
    conversation_id VARCHAR NOT NULL,
    model VARCHAR NOT NULL,
    cost_usd DOUBLE,
    sample_count BIGINT NOT NULL,
    PRIMARY KEY (bucket_start, conversation_id, model)
);
CREATE TABLE IF NOT EXISTS archive.codex_turn_cost (
    conversation_id VARCHAR NOT NULL,
    turn_id VARCHAR NOT NULL,
    ts TIMESTAMP NOT NULL,
    received_at TIMESTAMP NOT NULL,
    model VARCHAR,
    estimated_usd VARCHAR NOT NULL,
    PRIMARY KEY (conversation_id, turn_id)
);
CREATE TABLE IF NOT EXISTS archive.codex_skill_invocation (
    conversation_id VARCHAR NOT NULL,
    turn_id VARCHAR NOT NULL,
    skill_name VARCHAR NOT NULL,
    plugin_id VARCHAR NOT NULL,
    invocation_type VARCHAR NOT NULL,
    ts TIMESTAMP NOT NULL,
    scope VARCHAR,
    PRIMARY KEY (conversation_id, turn_id, skill_name, plugin_id, invocation_type)
);
CREATE TABLE IF NOT EXISTS archive.codex_ttft_histogram (
    bucket_start TIMESTAMP NOT NULL,
    conversation_id VARCHAR NOT NULL,
    model VARCHAR NOT NULL,
    ttft_ms BIGINT NOT NULL,
    sample_count BIGINT NOT NULL,
    PRIMARY KEY (bucket_start, conversation_id, model, ttft_ms)
);
CREATE TABLE IF NOT EXISTS archive.codex_turn_usage_hourly (
    bucket_start TIMESTAMP NOT NULL,
    model VARCHAR NOT NULL,
    token_type VARCHAR NOT NULL,
    session_source VARCHAR NOT NULL,
    sample_count BIGINT NOT NULL,
    sum_tokens DOUBLE NOT NULL,
    PRIMARY KEY (bucket_start, model, token_type, session_source)
);
CREATE TABLE IF NOT EXISTS archive.codex_legacy_cost_hourly (
    bucket_start TIMESTAMP NOT NULL,
    model VARCHAR NOT NULL,
    cost_usd DOUBLE NOT NULL,
    PRIMARY KEY (bucket_start, model)
);
CREATE TABLE IF NOT EXISTS archive.codex_legacy_session_cost (
    conversation_id VARCHAR PRIMARY KEY,
    cost_usd DOUBLE NOT NULL
);
INSERT INTO archive.codex_legacy_cost_hourly
SELECT bucket_start, model, cost_usd FROM archive.usage_hourly WHERE client = 'codex'
ON CONFLICT DO NOTHING;
INSERT INTO archive.codex_legacy_session_cost
SELECT session_id, cost_usd FROM archive.sessions WHERE client = 'codex'
ON CONFLICT DO NOTHING;

CREATE OR REPLACE VIEW codex_native_turn_costs AS
SELECT ts, received_at, conversation_id, turn_id, model,
       CAST(estimated_usd AS DECIMAL(38,18)) cost_usd
FROM (
    SELECT ts, received_at, conversation_id, turn_id, model, estimated_usd FROM codex_event_turn_cost
    UNION ALL
    SELECT ts, received_at, conversation_id, turn_id, model, estimated_usd FROM archive.codex_turn_cost
) native_cost
WHERE NULLIF(conversation_id, '') IS NOT NULL AND NULLIF(turn_id, '') IS NOT NULL
  AND TRY_CAST(estimated_usd AS DECIMAL(38,18)) >= 0
QUALIFY row_number() OVER (PARTITION BY conversation_id, turn_id ORDER BY ts DESC, received_at DESC, CAST(estimated_usd AS DECIMAL(38,18)) DESC) = 1;

CREATE OR REPLACE VIEW codex_local_costs AS
SELECT ts, conversation_id, model, cost_usd, CASE WHEN cost_usd IS NULL THEN 0 ELSE 1 END sample_count
FROM codex_valid_token_usage
UNION ALL
SELECT bucket_start, NULLIF(conversation_id, ''), NULLIF(model, ''), cost_usd, sample_count
FROM archive.codex_local_cost_hourly;

CREATE OR REPLACE VIEW codex_selected_costs AS
SELECT ts, conversation_id, model, CAST(cost_usd AS DOUBLE) cost_usd, 'native' cost_source, 1 sample_count
FROM codex_native_turn_costs
UNION ALL
SELECT ts, conversation_id, model, cost_usd, 'local', sample_count FROM codex_local_costs l
WHERE NOT EXISTS (SELECT 1 FROM codex_native_turn_costs n WHERE n.conversation_id = l.conversation_id)
UNION ALL
SELECT bucket_start, NULL, NULLIF(model, ''), cost_usd, 'legacy', CASE WHEN cost_usd > 0 THEN 1 ELSE 0 END
FROM archive.codex_legacy_cost_hourly;

CREATE OR REPLACE VIEW codex_skill_invocations AS
SELECT conversation_id, turn_id, skill_name, COALESCE(plugin_id, '') plugin_id, invocation_type,
       MIN(ts) ts, MIN(scope) AS scope
FROM (
    SELECT conversation_id, turn_id, skill_name, plugin_id, invocation_type, ts, scope FROM codex_event_skill_invocation
    UNION ALL
    SELECT conversation_id, turn_id, skill_name, plugin_id, invocation_type, ts, scope FROM archive.codex_skill_invocation
) skills
GROUP BY 1, 2, 3, 4, 5;
