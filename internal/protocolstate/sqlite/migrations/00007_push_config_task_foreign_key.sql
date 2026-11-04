-- +goose Up
DELETE FROM a2a_push_configs
WHERE NOT EXISTS (
    SELECT 1 FROM a2a_tasks WHERE a2a_tasks.task_id = a2a_push_configs.task_id
);

CREATE TABLE a2a_push_configs_owned (
    task_id TEXT NOT NULL,
    config_id TEXT NOT NULL,
    tenant TEXT NOT NULL,
    principal TEXT NOT NULL,
    owner_key TEXT NOT NULL,
    url TEXT NOT NULL,
    updated_at_unix INTEGER NOT NULL,
    config_json TEXT NOT NULL,
    PRIMARY KEY (task_id, config_id),
    FOREIGN KEY (task_id) REFERENCES a2a_tasks(task_id)
);

INSERT INTO a2a_push_configs_owned
    (task_id, config_id, tenant, principal, owner_key, url, updated_at_unix, config_json)
SELECT task_id, config_id, tenant, principal, owner_key, url, updated_at_unix, config_json
FROM a2a_push_configs;

DROP TABLE a2a_push_configs;
ALTER TABLE a2a_push_configs_owned RENAME TO a2a_push_configs;

CREATE INDEX idx_a2a_push_configs_owner_task
    ON a2a_push_configs(owner_key, tenant, task_id);

-- +goose Down
CREATE TABLE a2a_push_configs_unowned (
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

INSERT INTO a2a_push_configs_unowned
    (task_id, config_id, tenant, principal, owner_key, url, updated_at_unix, config_json)
SELECT task_id, config_id, tenant, principal, owner_key, url, updated_at_unix, config_json
FROM a2a_push_configs;

DROP TABLE a2a_push_configs;
ALTER TABLE a2a_push_configs_unowned RENAME TO a2a_push_configs;

CREATE INDEX idx_a2a_push_configs_owner_task
    ON a2a_push_configs(owner_key, tenant, task_id);
