
insert into projects (slug,name,description,governance_model,license,created_at,updated_at,visibility,public_id,repo_url,repo_host)
 values ('quiet','Quiet','x','collective','MIT', now()-interval '400 days', now(), 'public', 'p-quiet', 'https://example.invalid/quiet.git','github'),
        ('never-read','Never Read','x','collective','MIT', now()-interval '400 days', now(), 'public', 'p-never', 'https://example.invalid/never.git','github'),
        ('norepo','No Repo','x','collective','MIT', now()-interval '400 days', now(), 'public', 'p-norepo', null,null);
insert into project_metrics (project_id, contributors, last_commit_at, health_score, computed_at)
 select id, 1, now()-interval '120 days', 0.5, now() from projects where slug='quiet';
