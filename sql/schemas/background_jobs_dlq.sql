create table public.background_jobs_dlq (
  id uuid not null default gen_random_uuid (),
  original_job_id uuid null,
  job_type text not null,
  payload jsonb not null default '{}'::jsonb,
  attempts integer not null default 0,
  max_attempts integer not null default 0,
  error_message text null,
  failed_at timestamp with time zone not null default now(),
  constraint background_jobs_dlq_pkey primary key (id),
  constraint background_jobs_dlq_original_job_id_fkey foreign KEY (original_job_id) references background_jobs (id) on delete set null
) TABLESPACE pg_default;

create index if not exists idx_background_jobs_dlq_failed_at on public.background_jobs_dlq using btree (failed_at desc) TABLESPACE pg_default;
