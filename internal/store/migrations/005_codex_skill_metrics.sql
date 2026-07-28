-- @author Kurok1 <im.kurokyhanc@gmail.com>
-- @since v2.5.0

CREATE TABLE codex_metric_skill_injected (
    ts               TIMESTAMP NOT NULL,
    start_ts         TIMESTAMP NOT NULL,
    received_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    value            BIGINT NOT NULL,
    conversation_id  VARCHAR,
    app_version      VARCHAR,
    auth_mode        VARCHAR,
    originator       VARCHAR,
    terminal_type    VARCHAR,
    model            VARCHAR,
    slug             VARCHAR,
    user_account_id  VARCHAR,
    user_email        VARCHAR,
    skill            VARCHAR,
    status           VARCHAR,
    invoke_type      VARCHAR,
    session_source   VARCHAR,
    attrs            VARCHAR
);
