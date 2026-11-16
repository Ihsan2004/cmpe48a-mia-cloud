create table public.password_reset_tokens (
  token text not null,
  user_id uuid not null,
  expires_at timestamp with time zone not null,
  created_at timestamp with time zone null default now(),
  constraint password_reset_tokens_pkey primary key (token),
  constraint password_reset_tokens_user_id_fkey foreign KEY (user_id) references users (id) on delete CASCADE
) TABLESPACE pg_default;

create index IF not exists idx_password_reset_tokens_user_id on public.password_reset_tokens using btree (user_id) TABLESPACE pg_default;