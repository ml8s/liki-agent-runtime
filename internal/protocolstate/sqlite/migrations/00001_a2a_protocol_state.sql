-- +goose Up
CREATE TABLE a2a_tasks (
    task_id TEXT PRIMARY KEY,
    tenant TEXT NOT NULL,
    principal TEXT NOT NULL,
    owner_key TEXT NOT NULL,
    context_id TEXT NOT NULL,
    state TEXT NOT NULL,
    status_at_unix INTEGER,
    updated_at_unix INTEGER NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    task_json TEXT NOT NULL
);

CREATE INDEX idx_a2a_tasks_owner_context
    ON a2a_tasks(owner_key, tenant, context_id);
CREATE INDEX idx_a2a_tasks_owner_state
    ON a2a_tasks(owner_key, tenant, state, status_at_unix);
CREATE INDEX idx_a2a_tasks_owner_updated
    ON a2a_tasks(owner_key, tenant, updated_at_unix DESC, task_id DESC);

CREATE TABLE a2a_push_configs (
    task_id TEXT NOT NULL,
    config_id TEXT NOT NULL,
    tenant TEXT NOT NULL,
    principal TEXT NOT NULL,
    owner_key TEXT NOT NULL,
    url TEXT NOT NULL,
    updated_at_unix INTEGER NOT NULL,
    config_json TEXT NOT NULL,
    PRIMARY KEY (task_id, config_id)
);

CREATE INDEX idx_a2a_push_configs_owner_task
    ON a2a_push_configs(owner_key, tenant, task_id);

-- +goose Down
DROP TABLE a2a_push_configs;
DROP TABLE a2a_tasks;
