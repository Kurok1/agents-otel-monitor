-- @author Kurok1 <im.kurokyhanc@gmail.com>
-- @since 3.1.0

CREATE SCHEMA IF NOT EXISTS archive;

SET schema 'archive';

CREATE TABLE IF NOT EXISTS usage_hourly (
    bucket_start            TIMESTAMP NOT NULL,
    client                  VARCHAR NOT NULL,
    model                   VARCHAR NOT NULL,
    tokens_total            BIGINT NOT NULL DEFAULT 0,
    input_tokens            BIGINT NOT NULL DEFAULT 0,
    output_tokens           BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens       BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens   BIGINT NOT NULL DEFAULT 0,
    reasoning_tokens        BIGINT NOT NULL DEFAULT 0,
    throughput_input_tokens BIGINT NOT NULL DEFAULT 0,
    token_rows              BIGINT NOT NULL DEFAULT 0,
    request_count           BIGINT NOT NULL DEFAULT 0,
    cost_usd                DOUBLE NOT NULL DEFAULT 0,
    speed_units             DOUBLE NOT NULL DEFAULT 0,
    speed_duration_ms       DOUBLE NOT NULL DEFAULT 0,
    model_last_seen         TIMESTAMP,
    PRIMARY KEY (bucket_start, client, model)
);

CREATE TABLE IF NOT EXISTS tool_hourly (
    bucket_start TIMESTAMP NOT NULL,
    client       VARCHAR NOT NULL,
    tool_name    VARCHAR NOT NULL,
    call_count   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket_start, client, tool_name)
);

CREATE TABLE IF NOT EXISTS skill_hourly (
    bucket_start     TIMESTAMP NOT NULL,
    client           VARCHAR NOT NULL,
    skill_name       VARCHAR NOT NULL,
    activation_count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket_start, client, skill_name)
);

CREATE TABLE IF NOT EXISTS sessions (
    client                VARCHAR NOT NULL,
    session_id            VARCHAR NOT NULL,
    first_active          TIMESTAMP,
    last_active           TIMESTAMP,
    tokens_total          BIGINT NOT NULL DEFAULT 0,
    input_tokens          BIGINT NOT NULL DEFAULT 0,
    output_tokens         BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT NOT NULL DEFAULT 0,
    reasoning_tokens      BIGINT NOT NULL DEFAULT 0,
    request_count         BIGINT NOT NULL DEFAULT 0,
    tool_calls            BIGINT NOT NULL DEFAULT 0,
    skill_activations     BIGINT NOT NULL DEFAULT 0,
    cost_usd              DOUBLE NOT NULL DEFAULT 0,
    PRIMARY KEY (client, session_id)
);

CREATE TABLE IF NOT EXISTS session_tools (
    client       VARCHAR NOT NULL,
    session_id   VARCHAR NOT NULL,
    tool_name    VARCHAR NOT NULL,
    call_count   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (client, session_id, tool_name)
);

CREATE TABLE IF NOT EXISTS session_skills (
    client           VARCHAR NOT NULL,
    session_id       VARCHAR NOT NULL,
    skill_name       VARCHAR NOT NULL,
    activation_count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (client, session_id, skill_name)
);

CREATE TABLE IF NOT EXISTS maintenance_state (
    id                SMALLINT PRIMARY KEY CHECK (id = 1),
    last_cutoff_ts    TIMESTAMP,
    last_success_at   TIMESTAMP,
    last_deleted_rows BIGINT NOT NULL DEFAULT 0
);

SET schema 'main';
