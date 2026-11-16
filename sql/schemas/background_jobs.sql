create table public.background_jobs (
  id uuid not null default gen_random_uuid (),
  job_type text not null,
  payload jsonb not null default '{}'::jsonb,
  status text not null default 'pending'::text,
  attempts integer not null default 0,
  max_attempts integer not null default 5,
  scheduled_at timestamp with time zone not null default now(),
  started_at timestamp with time zone null,
  finished_at timestamp with time zone null,
  locked_by text null,
  locked_at timestamp with time zone null,
  last_error text null,
  created_at timestamp with time zone not null default now(),
  updated_at timestamp with time zone not null default now(),
  constraint background_jobs_pkey primary key (id),
  constraint background_jobs_status_check check (
    (
      status = any (
        array[
          'pending'::text,
          'processing'::text,
          'completed'::text,
          'dead'::text
        ]
      )
    )
  ),
  constraint background_jobs_attempts_check check ((attempts >= 0)),
  constraint background_jobs_max_attempts_check check ((max_attempts > 0))
) TABLESPACE pg_default;

create index if not exists idx_background_jobs_pending_schedule on public.background_jobs using btree (status, scheduled_at, created_at) TABLESPACE pg_default
where
  (status = 'pending'::text);

create index if not exists idx_background_jobs_type_status on public.background_jobs using btree (job_type, status, created_at desc) TABLESPACE pg_default;
