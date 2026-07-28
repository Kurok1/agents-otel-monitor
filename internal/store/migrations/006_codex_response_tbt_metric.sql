-- @author Kurok1 <im.kurokyhanc@gmail.com>
-- @since v2.5.0

CREATE TABLE codex_metric_response_tbt (
    ts               TIMESTAMP NOT NULL,
    start_ts         TIMESTAMP NOT NULL,
    received_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    sample_count     BIGINT NOT NULL,
    sum_ms           DOUBLE NOT NULL,
    conversation_id  VARCHAR,
    app_version      VARCHAR,
    auth_mode        VARCHAR,
    originator       VARCHAR,
    terminal_type    VARCHAR,
    model            VARCHAR,
    slug             VARCHAR,
    user_account_id  VARCHAR,
    user_email        VARCHAR,
    session_source   VARCHAR,
    attrs            VARCHAR
);
