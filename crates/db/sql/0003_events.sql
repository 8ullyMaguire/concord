-- Append-only event log. One row is one state change that mattered; no UPDATE or DELETE statement exists in the app.
create table if not exists events (
    id bigserial primary key,
    project_id bigint references projects(id) on delete set null,
    actor_id bigint references users(id) on delete set null,
    kind text not null,
    entity text not null,
    entity_id bigint not null,
    detail text not null default '',
    created_at timestamptz not null default now()
);

create index if not exists events_project_created on events (project_id, created_at desc, id desc);
create index if not exists events_kind_created on events (kind, created_at desc, id desc);
