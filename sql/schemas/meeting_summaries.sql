create table public.meeting_summaries (
  id uuid not null default gen_random_uuid (),
  meeting_id uuid not null,
  content text null,
  created_at timestamp with time zone null default now(),
  updated_at timestamp with time zone null default now(),
  summary_type text null default 'final_detailed'::text,
  processing_status text null default 'completed'::text,
  retry_count integer null default 0,
  coverage_ratio double precision null,
  successful_chunks integer null,
  failed_chunks integer null,
  summary_source text null,
  constraint meeting_summaries_pkey primary key (id),
  constraint meeting_summaries_meeting_id_fkey foreign KEY (meeting_id) references meetings (id) on delete CASCADE,
  constraint meeting_summaries_processing_status_check check (
    (
      processing_status = any (
        array[
          'pending'::text,
          'processing'::text,
          'completed'::text,
          'completed_with_gaps'::text,
          'failed'::text
        ]
      )
    )
  ),
  constraint meeting_summaries_summary_type_check check (
    (
      summary_type = any (
        array[
          'interim'::text,
          'final_detailed'::text,
          'final_brief'::text
        ]
      )
    )
  )
) TABLESPACE pg_default;

create index IF not exists idx_meeting_summaries_source on public.meeting_summaries using btree (summary_source, created_at desc) TABLESPACE pg_default
where
  (summary_type = 'final_detailed'::text);

create index IF not exists idx_meeting_summaries_meeting_id on public.meeting_summaries using btree (meeting_id) TABLESPACE pg_default;

create index IF not exists idx_meeting_summaries_status on public.meeting_summaries using btree (meeting_id, summary_type, processing_status) TABLESPACE pg_default;

create unique INDEX IF not exists idx_meeting_summaries_unique_type on public.meeting_summaries using btree (meeting_id, summary_type) TABLESPACE pg_default;

create index IF not exists idx_meeting_summaries_type on public.meeting_summaries using btree (meeting_id, summary_type) TABLESPACE pg_default;