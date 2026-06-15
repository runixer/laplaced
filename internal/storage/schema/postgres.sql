-- Consolidated PostgreSQL schema.
--
-- This is the cumulative end-state of the SQLite bootstrap DDL + migrations
-- 001–013, translated to PostgreSQL. PostgreSQL is a greenfield backend, so
-- there is no incremental migration replay — this single file IS the schema.
--
-- Type mapping vs SQLite:
--   INTEGER PRIMARY KEY AUTOINCREMENT  -> BIGINT GENERATED ALWAYS AS IDENTITY
--   user_id / scope partition key      -> uuid          (ScopeID; SQLite: TEXT)
--   entity ids (message/topic/fact/…)  -> bigint
--   embedding BLOB                     -> bytea
--   REAL                               -> double precision
--   BOOLEAN DEFAULT 0/1                -> boolean DEFAULT false/true
--   TIMESTAMP DEFAULT CURRENT_TIMESTAMP-> timestamptz DEFAULT now()
--
-- Foreign keys are intentionally OMITTED: SQLite never enforced them (no
-- PRAGMA foreign_keys=ON), so adding enforced FKs on PostgreSQL would change
-- behavior (e.g. an artifact insert for a scope with no users row would fail).
-- Isolation is enforced in application SQL (WHERE user_id = ?), not by the DB.

CREATE TABLE IF NOT EXISTS users (
    id          uuid PRIMARY KEY,
    username    text,
    first_name  text,
    last_name   text,
    last_seen   timestamptz DEFAULT now()
);

CREATE TABLE IF NOT EXISTS history (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         uuid NOT NULL,
    role            text NOT NULL,
    content         text NOT NULL,
    topic_id        bigint,
    created_at      timestamptz DEFAULT now(),
    author          text,
    message_id      text,
    conversation_id text,
    thread_root     text,
    trace_id        text
);
CREATE INDEX IF NOT EXISTS idx_history_user_id ON history(user_id);
CREATE INDEX IF NOT EXISTS idx_history_topic_id ON history(topic_id);
-- Retrofit columns added after a DB was first created. The consolidated schema
-- above is pure CREATE IF NOT EXISTS, so it never alters an existing table; these
-- idempotent ALTERs bring older Postgres DBs up to the current shape on startup.
ALTER TABLE history ADD COLUMN IF NOT EXISTS trace_id text;

CREATE TABLE IF NOT EXISTS stats (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     uuid NOT NULL,
    tokens_used integer NOT NULL,
    cost_usd    double precision NOT NULL,
    recorded_at timestamptz DEFAULT now()
);

CREATE TABLE IF NOT EXISTS rag_logs (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id           uuid NOT NULL,
    original_query    text,
    enriched_query    text,
    enrichment_prompt text,
    context_used      text,
    system_prompt     text,
    retrieval_results text,
    llm_response      text,
    enrichment_tokens integer,
    generation_tokens integer,
    total_cost_usd    double precision,
    created_at        timestamptz DEFAULT now()
);

CREATE TABLE IF NOT EXISTS topics (
    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id               uuid NOT NULL,
    summary               text,
    start_msg_id          bigint NOT NULL,
    end_msg_id            bigint NOT NULL,
    size_chars            integer DEFAULT 0,
    embedding             bytea,
    facts_extracted       boolean DEFAULT false,
    is_consolidated       boolean DEFAULT false,
    consolidation_checked boolean DEFAULT false,
    embedding_version     text,
    created_at            timestamptz DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_topics_user_id ON topics(user_id);

CREATE TABLE IF NOT EXISTS memory_bank (
    user_id    uuid PRIMARY KEY,
    content    text,
    updated_at timestamptz DEFAULT now()
);

CREATE TABLE IF NOT EXISTS structured_facts (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id           uuid NOT NULL,
    relation          text NOT NULL,
    category          text NOT NULL,
    content           text NOT NULL,
    type              text NOT NULL,
    importance        integer NOT NULL,
    embedding         bytea,
    topic_id          bigint,
    embedding_version text,
    created_at        timestamptz DEFAULT now(),
    last_updated      timestamptz DEFAULT now(),
    UNIQUE(user_id, relation, content)
);
CREATE INDEX IF NOT EXISTS idx_structured_facts_category ON structured_facts(user_id, category);
CREATE INDEX IF NOT EXISTS idx_structured_facts_type ON structured_facts(user_id, type);
CREATE INDEX IF NOT EXISTS idx_structured_facts_topic_id ON structured_facts(topic_id);

CREATE TABLE IF NOT EXISTS fact_history (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    fact_id       bigint NOT NULL,
    user_id       uuid NOT NULL,
    action        text NOT NULL,
    old_content   text,
    new_content   text,
    reason        text,
    category      text,
    importance    integer DEFAULT 0,
    topic_id      bigint,
    relation      text,
    request_input text,
    created_at    timestamptz DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_fact_history_user_id ON fact_history(user_id);
CREATE INDEX IF NOT EXISTS idx_fact_history_fact_id ON fact_history(fact_id);
CREATE INDEX IF NOT EXISTS idx_fact_history_topic_id ON fact_history(topic_id);

CREATE TABLE IF NOT EXISTS reranker_logs (
    id                     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id                uuid NOT NULL,
    original_query         text,
    enriched_query         text,
    candidates_json        text,
    tool_calls_json        text,
    selected_ids_json      text,
    reasoning_json         text,
    fallback_reason        text,
    duration_enrichment_ms integer,
    duration_vector_ms     integer,
    duration_reranker_ms   integer,
    duration_total_ms      integer,
    created_at             timestamptz DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_reranker_logs_user_id ON reranker_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_reranker_logs_created_at ON reranker_logs(created_at DESC);

CREATE TABLE IF NOT EXISTS agent_logs (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id            uuid NOT NULL,
    agent_type         text NOT NULL,
    input_prompt       text,
    input_context      text,
    output_response    text,
    output_parsed      text,
    output_context     text,
    model              text,
    prompt_tokens      integer,
    completion_tokens  integer,
    total_cost         double precision,
    duration_ms        integer,
    metadata           text,
    success            boolean DEFAULT true,
    error_message      text,
    conversation_turns text,
    created_at         timestamptz DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_logs_user_id ON agent_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_agent_logs_agent_type ON agent_logs(agent_type);
CREATE INDEX IF NOT EXISTS idx_agent_logs_created_at ON agent_logs(created_at DESC);

CREATE TABLE IF NOT EXISTS people (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id           uuid NOT NULL,
    display_name      text NOT NULL,
    aliases           text DEFAULT '[]',
    telegram_id       bigint,
    username          text,
    circle            text DEFAULT 'Other',
    bio               text,
    embedding         bytea,
    embedding_version text,
    first_seen        timestamptz DEFAULT now(),
    last_seen         timestamptz DEFAULT now(),
    mention_count     integer DEFAULT 1,
    external_transport text,
    external_id        text,
    UNIQUE(user_id, display_name)
);
CREATE INDEX IF NOT EXISTS idx_people_user_id ON people(user_id);
CREATE INDEX IF NOT EXISTS idx_people_telegram_id ON people(user_id, telegram_id);
CREATE INDEX IF NOT EXISTS idx_people_username ON people(user_id, username);
CREATE INDEX IF NOT EXISTS idx_people_circle ON people(user_id, circle);
CREATE INDEX IF NOT EXISTS idx_people_external_id ON people(user_id, external_transport, external_id);

CREATE TABLE IF NOT EXISTS artifacts (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id            uuid NOT NULL,
    message_id         bigint NOT NULL,
    file_type          text NOT NULL,
    file_path          text NOT NULL,
    file_size          bigint,
    mime_type          text,
    original_name      text,
    content_hash       text NOT NULL,
    state              text NOT NULL DEFAULT 'pending',
    error_message      text,
    retry_count        integer DEFAULT 0,
    last_failed_at     timestamptz,
    summary            text,
    keywords           text,
    entities           text,
    rag_hints          text,
    embedding          bytea,
    embedding_version  text,
    created_at         timestamptz DEFAULT now(),
    processed_at       timestamptz,
    context_load_count integer DEFAULT 0,
    last_loaded_at     timestamptz,
    user_context       text,
    UNIQUE(user_id, content_hash)
);
CREATE INDEX IF NOT EXISTS idx_artifacts_user_id ON artifacts(user_id);
CREATE INDEX IF NOT EXISTS idx_artifacts_status ON artifacts(user_id, state);
CREATE INDEX IF NOT EXISTS idx_artifacts_hash ON artifacts(user_id, content_hash);
CREATE INDEX IF NOT EXISTS idx_artifacts_message_id ON artifacts(user_id, message_id);
CREATE INDEX IF NOT EXISTS idx_artifacts_type ON artifacts(user_id, file_type);

-- Principal identity model. Four normalized tables:
--   scopes      thin registry of memory tenants (id = partition key).
--   identities  (transport, native_id) -> scope_id map; many handles per principal.
--   principals  AD-backed person details, dedup'd by object_guid then ad_login.
--   channels    channel scope details, keyed by (transport, native_id).
-- Passthrough scopes (home Telegram, resolver-less transports) write NO scopes
-- row: their id is a deterministic uuidv5 and an absent row means a person scope.
CREATE TABLE IF NOT EXISTS scopes (
    id           uuid PRIMARY KEY,
    scope_type   text NOT NULL DEFAULT 'user',  -- 'user' | 'channel' | 'principal'
    display_name text,
    created_at   timestamptz DEFAULT now(),
    last_seen    timestamptz DEFAULT now()
);

CREATE TABLE IF NOT EXISTS identities (
    transport  text NOT NULL,
    native_id  text NOT NULL,
    scope_id   uuid NOT NULL,
    created_at timestamptz DEFAULT now(),
    last_seen  timestamptz DEFAULT now(),
    PRIMARY KEY (transport, native_id)
);
CREATE INDEX IF NOT EXISTS idx_identities_scope_id ON identities(scope_id);

CREATE TABLE IF NOT EXISTS principals (
    scope_id     uuid PRIMARY KEY,
    object_guid  text,
    ad_login     text,
    email        text,
    display_name text,
    created_at   timestamptz DEFAULT now(),
    updated_at   timestamptz DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_principals_object_guid ON principals(object_guid) WHERE object_guid IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_principals_ad_login ON principals(ad_login) WHERE ad_login IS NOT NULL;

CREATE TABLE IF NOT EXISTS channels (
    scope_id     uuid PRIMARY KEY,
    transport    text NOT NULL,
    native_id    text NOT NULL,
    display_name text,
    created_at   timestamptz DEFAULT now(),
    last_seen    timestamptz DEFAULT now(),
    UNIQUE(transport, native_id)
);

CREATE TABLE IF NOT EXISTS response_flags (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       uuid NOT NULL,
    history_id    bigint,
    message_id    text,
    trace_id      text,
    emoji         text,
    reply_preview text,
    created_at    timestamptz DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_response_flags_user_id ON response_flags(user_id);
CREATE INDEX IF NOT EXISTS idx_response_flags_created_at ON response_flags(created_at DESC);
