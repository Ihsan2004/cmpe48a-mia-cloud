-- ============================================================
-- Test database initial schema dump
-- ============================================================
--
-- Source: prod Supabase project (`srckgntevwtwoqhjunfu`)
-- Generated: 2026-04-26 via pg_dump --schema-only --no-owner --no-acl
-- Includes: current public tables (users → profiles → meetings → quota),
--           all indexes, RLS policies, functions, triggers.
-- Excludes: row data, ownership/permission grants (yeni Supabase
--           project'inde Supabase kendi role'lerini kullanır).
--
-- HOW TO USE:
--   1. New Supabase project oluştur (free tier yeter — Settings → Database)
--   2. Bu dosyanın TÜMÜNÜ Supabase Dashboard → SQL Editor'e yapıştır
--   3. Run → schema oluşur (1-2 saniye)
--   4. Connection string'i al, GitHub Secrets → TEST_DATABASE_URL'i güncelle
--   5. CI'yi yeniden tetikle → integration testleri yeşil olmalı
--
-- Future migration testleri için: yeni .sql migration ilk burada test et,
-- sonra prod'a uygula. Phase 1 deploy'unda yaşadığımız "CI patladı çünkü
-- prod'a migration uygulanmamıştı" sorununu kalıcı önler.
--
-- Notes:
--  - Supabase auth/storage schemaları DAHİL DEĞİL — yeni project'te
--    Supabase bunları otomatik kurar
--  - meeting-audio bucket için 005b_storage_bucket.sql ayrıca çalıştırılmalı
--    (storage schema'ya yazıyor, public dump'ta yok)
-- ============================================================

--
-- PostgreSQL database dump
--



-- Dumped from database version 17.6
-- Dumped by pg_dump version 18.3

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: public; Type: SCHEMA; Schema: -; Owner: -
--
-- Supabase auto-creates the `public` schema on every new project so
-- the pg_dump CREATE SCHEMA below would error 42P06 ("already exists").
-- Wrap in IF NOT EXISTS to keep the dump idempotent.

CREATE SCHEMA IF NOT EXISTS public;


--
-- Required extensions
--
-- The schema uses:
--   - gen_random_uuid()  → pgcrypto
--   - vector(1536) + HNSW index + vector_cosine_ops → pgvector
-- Both ship with Supabase but aren't enabled by default. CREATE
-- EXTENSION IF NOT EXISTS keeps the dump idempotent on re-runs.

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA extensions;
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;


--
-- Name: SCHEMA public; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON SCHEMA public IS 'standard public schema';


--
-- Name: handle_new_user(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.handle_new_user() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO public.profiles (id, full_name, email)
    VALUES (NEW.id, NEW.full_name, NEW.email)
    ON CONFLICT (id) DO NOTHING;
    RETURN NEW;
END;
$$;


--
-- Name: update_calendar_connection_status(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_calendar_connection_status() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.google_refresh_token IS NOT NULL AND NEW.google_refresh_token != '' THEN
        NEW.is_calendar_connected = true;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: update_calendar_subscriptions_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_calendar_subscriptions_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;


--
-- Name: update_users_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_users_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;


--
-- Name: quota_cycle_for(smallint, timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.quota_cycle_for(p_anchor_day smallint, p_at timestamp with time zone) RETURNS TABLE(cycle_start timestamp with time zone, cycle_end timestamp with time zone)
    LANGUAGE plpgsql IMMUTABLE
    AS $$
DECLARE
    at_utc       TIMESTAMP;
    month_start  DATE;
    eom_day      INT;
    start_date   DATE;
    end_month    DATE;
    end_eom_day  INT;
    end_date     DATE;
BEGIN
    at_utc      := (p_at AT TIME ZONE 'UTC');
    month_start := date_trunc('month', at_utc)::DATE;

    eom_day := EXTRACT(DAY FROM
        (month_start + INTERVAL '1 month' - INTERVAL '1 day')
    )::INT;
    start_date := month_start + (LEAST(p_anchor_day::INT, eom_day) - 1);

    IF start_date::TIMESTAMP > at_utc THEN
        month_start := (month_start - INTERVAL '1 month')::DATE;
        eom_day := EXTRACT(DAY FROM
            (month_start + INTERVAL '1 month' - INTERVAL '1 day')
        )::INT;
        start_date := month_start + (LEAST(p_anchor_day::INT, eom_day) - 1);
    END IF;

    end_month := date_trunc(
        'month',
        (start_date::TIMESTAMP + INTERVAL '1 month')
    )::DATE;
    end_eom_day := EXTRACT(DAY FROM
        (end_month + INTERVAL '1 month' - INTERVAL '1 day')
    )::INT;
    end_date := end_month + (LEAST(p_anchor_day::INT, end_eom_day) - 1);

    RETURN QUERY SELECT
        (start_date::TIMESTAMP AT TIME ZONE 'UTC'),
        (end_date::TIMESTAMP   AT TIME ZONE 'UTC');
END;
$$;


--
-- Name: update_user_quotas_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_user_quotas_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;


--
-- Name: create_user_quota_on_signup(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.create_user_quota_on_signup() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO public.user_quotas (user_id, tier, cycle_anchor_day)
    VALUES (
        NEW.id,
        'free',
        GREATEST(1, LEAST(31,
            EXTRACT(DAY FROM COALESCE(NEW.created_at, now()) AT TIME ZONE 'UTC')::INT
        ))::SMALLINT
    )
    ON CONFLICT (user_id) DO NOTHING;
    RETURN NEW;
END;
$$;


--
-- Name: upload_sessions_set_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.upload_sessions_set_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: background_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.background_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    job_type text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 5 NOT NULL,
    scheduled_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    locked_by text,
    locked_at timestamp with time zone,
    last_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT background_jobs_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT background_jobs_max_attempts_check CHECK ((max_attempts > 0)),
    CONSTRAINT background_jobs_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'completed'::text, 'dead'::text])))
);


--
-- Name: background_jobs_dlq; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.background_jobs_dlq (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    original_job_id uuid,
    job_type text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 0 NOT NULL,
    error_message text,
    failed_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: brief_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.brief_deliveries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    meeting_id uuid NOT NULL,
    user_id uuid NOT NULL,
    channel text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    last_error text,
    provider_message_id text,
    scheduled_for timestamp with time zone NOT NULL,
    sent_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT brief_deliveries_channel_check CHECK ((channel = ANY (ARRAY['email'::text, 'slack'::text]))),
    CONSTRAINT brief_deliveries_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'sent'::text, 'failed'::text, 'skipped'::text])))
);


--
-- Name: brief_feedback; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.brief_feedback (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    target_meeting_id uuid NOT NULL,
    brief_id uuid,
    feedback text NOT NULL,
    note text,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    CONSTRAINT brief_feedback_feedback_check CHECK ((feedback = ANY (ARRAY['useful'::text, 'not_useful'::text])))
);


--
-- Name: calendar_subscriptions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.calendar_subscriptions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    channel_id text NOT NULL,
    resource_id text NOT NULL,
    expiration timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: chain_decision_centroids; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.chain_decision_centroids (
    chain_id uuid NOT NULL,
    centroid public.vector(1536) NOT NULL,
    source_decision_count integer NOT NULL,
    computed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chain_decision_centroids_source_decision_count_check CHECK ((source_decision_count > 0))
);


--
-- Name: chain_decisions_v2; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.chain_decisions_v2 (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    meeting_id uuid NOT NULL,
    candidate_chain_id uuid,
    matched_meeting_id uuid,
    candidate_key text NOT NULL,
    algorithm_version text DEFAULT 'chain-v3'::text NOT NULL,
    mode text NOT NULL,
    status text NOT NULL,
    participant_overlap numeric(5,4),
    title_similarity numeric(5,4),
    weighted_score numeric(5,4),
    decision_cosine numeric(5,4),
    feature_scores jsonb DEFAULT '{}'::jsonb NOT NULL,
    llm_decision text,
    llm_confidence numeric(5,4),
    suppression_key text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    decided_at timestamp with time zone,
    CONSTRAINT chain_decisions_v2_llm_decision_check CHECK (((llm_decision IS NULL) OR (llm_decision = ANY (ARRAY['related'::text, 'not_related'::text])))),
    CONSTRAINT chain_decisions_v2_mode_check CHECK ((mode = ANY (ARRAY['shadow'::text, 'suggestion'::text, 'applied'::text]))),
    CONSTRAINT chain_decisions_v2_status_check CHECK ((status = ANY (ARRAY['shadow_only'::text, 'pending'::text, 'accepted'::text, 'declined'::text, 'expired'::text, 'suppressed'::text, 'applied'::text])))
);


--
-- Name: correction_dictionary; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.correction_dictionary (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    wrong_form text NOT NULL,
    correct_form text NOT NULL,
    description text,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: decision_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.decision_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    meeting_id uuid NOT NULL,
    chain_id uuid,
    user_id uuid NOT NULL,
    item_type text NOT NULL,
    text text NOT NULL,
    owner_name text,
    source_quote text,
    status text DEFAULT 'proposed'::text NOT NULL,
    confirmed_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    context text,
    alternatives_considered text,
    decision_timestamp_start double precision,
    decision_timestamp_end double precision,
    user_edited boolean DEFAULT false NOT NULL,
    CONSTRAINT decision_items_item_type_check CHECK ((item_type = ANY (ARRAY['decision'::text, 'action_item'::text, 'open_topic'::text]))),
    CONSTRAINT decision_items_status_check CHECK ((status = ANY (ARRAY['proposed'::text, 'confirmed'::text, 'completed'::text, 'dismissed'::text])))
);


--
-- Name: desktop_command_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.desktop_command_log (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    meeting_id uuid NOT NULL,
    user_id uuid NOT NULL,
    command_type text NOT NULL,
    sent_at timestamp with time zone DEFAULT now() NOT NULL,
    client_ack_status text,
    client_ack_at timestamp with time zone,
    -- Phase 2.4 R9b — `extended` ack added to keep init_test_db.sql
    -- in sync with migration 012. Fresh test/e2e DBs bootstrapped
    -- from this dump otherwise reject the recorder's force-stop
    -- extension ack even though migrated prod DBs accept it.
    CONSTRAINT desktop_command_log_ack_status_check CHECK (((client_ack_status IS NULL) OR (client_ack_status = ANY (ARRAY['accepted'::text, 'declined'::text, 'no_ack'::text, 'extended'::text])))),
    CONSTRAINT desktop_command_log_command_type_check CHECK ((command_type = ANY (ARRAY['start_recording'::text, 'stop_recording'::text, 'force_stop'::text])))
);


--
-- Name: desktop_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.desktop_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    connected_at timestamp with time zone DEFAULT now() NOT NULL,
    last_heartbeat_at timestamp with time zone DEFAULT now() NOT NULL,
    disconnected_at timestamp with time zone,
    client_version text
);


--
-- Name: meeting_briefs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.meeting_briefs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    target_meeting_id uuid NOT NULL,
    chain_id uuid,
    content text NOT NULL,
    generation_method text DEFAULT 'llm'::text NOT NULL,
    opened_at timestamp with time zone,
    email_sent_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    CONSTRAINT meeting_briefs_generation_method_check CHECK ((generation_method = ANY (ARRAY['llm'::text, 'template_fallback'::text])))
);


--
-- Name: meeting_chains; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.meeting_chains (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    chain_type text NOT NULL,
    calendar_series_id text,
    participant_hash text,
    display_name text,
    created_at timestamp with time zone DEFAULT now(),
    CONSTRAINT meeting_chains_chain_type_check CHECK ((chain_type = ANY (ARRAY['calendar_series'::text, 'participant_overlap_title'::text])))
);


--
-- Name: meeting_notes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.meeting_notes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    meeting_id uuid NOT NULL,
    user_id uuid NOT NULL,
    content text,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: meeting_summaries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.meeting_summaries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    meeting_id uuid NOT NULL,
    content text,
    summary_type text DEFAULT 'final_detailed'::text,
    processing_status text DEFAULT 'completed'::text,
    retry_count integer DEFAULT 0,
    coverage_ratio double precision,
    successful_chunks integer,
    failed_chunks integer,
    summary_source text,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    CONSTRAINT meeting_summaries_processing_status_check CHECK ((processing_status = ANY (ARRAY['pending'::text, 'processing'::text, 'completed'::text, 'completed_with_gaps'::text, 'failed'::text]))),
    CONSTRAINT meeting_summaries_summary_type_check CHECK ((summary_type = ANY (ARRAY['interim'::text, 'final_detailed'::text, 'final_brief'::text])))
);


--
-- Name: meetings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.meetings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    start_time timestamp with time zone NOT NULL,
    end_time timestamp with time zone NOT NULL,
    meeting_url text,
    google_event_id text NOT NULL,
    description text,
    location text,
    attendees jsonb DEFAULT '[]'::jsonb,
    organizer_email text,
    auto_bot_enabled boolean DEFAULT true,
    bot_status text,
    bot_joined_at timestamp with time zone,
    last_heartbeat_at timestamp with time zone,
    agenda_items text,
    expected_outcomes text,
    created_at timestamp with time zone DEFAULT now(),
    calendar_series_id text,
    chain_id uuid,
    processed_at timestamp with time zone,
    audio_storage_path text,
    audio_content_type text,
    audio_size_bytes bigint,
    audio_duration_seconds integer,
    audio_source text,
    transcription_status text DEFAULT 'pending'::text NOT NULL,
    transcription_provider_job_id text,
    transcription_attempt_count integer DEFAULT 0 NOT NULL,
    transcription_last_error text,
    transcription_completed_at timestamp with time zone,
    audio_deleted_at timestamp with time zone,
    recording_method_override text,
    -- Phase 2.5 B8 — detected_app column. Sync with migration 013.
    detected_app text,
    chain_assignment_method text,
    chain_assignment_score numeric(5,4),
    chain_assignment_reason jsonb,
    chain_assignment_locked_at timestamp with time zone,
    CONSTRAINT meetings_audio_source_check CHECK (((audio_source IS NULL) OR (audio_source = ANY (ARRAY['bot'::text, 'desktop'::text])))),
    CONSTRAINT meetings_chain_assignment_method_check CHECK (((chain_assignment_method IS NULL) OR (chain_assignment_method = ANY (ARRAY['series_id'::text, 'auto_score'::text, 'llm_suggestion'::text, 'decision_match'::text, 'manual_user'::text, 'manual_removed'::text])))),
    -- Phase 2.4 B5 — `skip` allowed at meeting level. Sync with migration 011.
    CONSTRAINT meetings_recording_method_override_check CHECK (((recording_method_override IS NULL) OR (recording_method_override = ANY (ARRAY['bot'::text, 'desktop'::text, 'skip'::text])))),
    CONSTRAINT meetings_transcription_status_check CHECK ((transcription_status = ANY (ARRAY['pending'::text, 'uploading'::text, 'processing'::text, 'completed'::text, 'failed'::text]))),
    -- Phase 2.5 B8 — detected_app allowlist. Sync with migration 013.
    CONSTRAINT meetings_detected_app_check CHECK (((detected_app IS NULL) OR (detected_app = ANY (ARRAY['zoom'::text, 'google_meet'::text, 'microsoft_teams'::text, 'webex'::text, 'slack_huddle'::text, 'discord'::text, 'facetime'::text, 'other'::text]))))
);


--
-- Name: password_reset_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.password_reset_tokens (
    token text NOT NULL,
    user_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.profiles (
    id uuid NOT NULL,
    full_name text,
    email text,
    job_title text,
    company text,
    timezone text DEFAULT 'UTC'::text,
    default_meeting_duration integer DEFAULT 30,
    work_start_hour text,
    work_end_hour text,
    bot_name text DEFAULT 'MIA Assistant'::text,
    bot_language text DEFAULT 'tr'::text,
    google_refresh_token text,
    is_calendar_connected boolean DEFAULT false,
    google_email text,
    auto_share_meetings boolean DEFAULT false,
    interface_language text DEFAULT 'tr'::text,
    updated_at timestamp with time zone DEFAULT now(),
    brief_lead_time_minutes integer DEFAULT 10,
    recording_method text DEFAULT 'bot'::text NOT NULL,
    brief_deliver_via_email boolean DEFAULT false NOT NULL,
    brief_deliver_via_slack boolean DEFAULT false NOT NULL,
    email_unsubscribed_at timestamp with time zone,
    CONSTRAINT profiles_recording_method_check CHECK ((recording_method = ANY (ARRAY['bot'::text, 'desktop'::text])))
);


--
-- Name: refresh_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.refresh_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    token text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked boolean DEFAULT false,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: slack_integrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.slack_integrations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    team_id text NOT NULL,
    team_name text,
    bot_user_id text NOT NULL,
    bot_token_encrypted text NOT NULL,
    dm_channel_id text,
    matched_email text,
    scope text NOT NULL,
    installed_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone
);


--
-- Name: summary_errors; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.summary_errors (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    meeting_id uuid NOT NULL,
    error_type text NOT NULL,
    error_message text NOT NULL,
    error_detail text,
    phase text NOT NULL,
    retry_count integer DEFAULT 0,
    resolved boolean DEFAULT false,
    created_at timestamp with time zone DEFAULT now(),
    resolved_at timestamp with time zone
);


--
-- Name: transcript_chat_blocks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.transcript_chat_blocks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    meeting_id uuid NOT NULL,
    speaker text NOT NULL,
    text text NOT NULL,
    start_time double precision NOT NULL,
    end_time double precision NOT NULL,
    raw_segment_ids jsonb,
    is_noise boolean DEFAULT false,
    embedding public.vector(1536),
    embedding_model character varying(50) DEFAULT 'text-embedding-3-small'::character varying,
    embedding_generated_at timestamp without time zone,
    is_finalized boolean DEFAULT false,
    finalized_at timestamp with time zone,
    source_segment_count integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: transcript_segments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.transcript_segments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    meeting_id uuid,
    meeting_url text,
    text text NOT NULL,
    start_time double precision NOT NULL,
    end_time double precision NOT NULL,
    duration double precision GENERATED ALWAYS AS ((end_time - start_time)) STORED,
    speaker text,
    role text NOT NULL,
    stt_provider text,
    metadata jsonb DEFAULT '{}'::jsonb,
    created_at timestamp with time zone DEFAULT now(),
    idempotency_key text
);


--
-- Name: upload_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.upload_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    meeting_id uuid NOT NULL,
    user_id uuid NOT NULL,
    content_type text NOT NULL,
    total_bytes bigint,
    received_bytes bigint DEFAULT 0 NOT NULL,
    storage_path text NOT NULL,
    status text DEFAULT 'in_progress'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    completed_at timestamp with time zone,
    CONSTRAINT upload_sessions_status_check CHECK ((status = ANY (ARRAY['in_progress'::text, 'completed'::text, 'aborted'::text])))
);


--
-- Name: usage_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    team_id uuid,
    meeting_id uuid,
    feature text NOT NULL,
    provider text NOT NULL,
    model text,
    input_tokens integer DEFAULT 0 NOT NULL,
    output_tokens integer DEFAULT 0 NOT NULL,
    audio_seconds double precision DEFAULT 0 NOT NULL,
    cost_usd double precision DEFAULT 0 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    password_hash text DEFAULT ''::text NOT NULL,
    email_verified boolean DEFAULT false,
    full_name text,
    avatar_url text,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    last_login_at timestamp with time zone,
    is_active boolean DEFAULT true,
    failed_login_attempts integer DEFAULT 0,
    locked_until timestamp with time zone
);


--
-- Name: user_quota_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_quota_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    actor text NOT NULL,
    old_snapshot jsonb,
    new_snapshot jsonb NOT NULL,
    reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_quotas; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_quotas (
    user_id uuid NOT NULL,
    tier text DEFAULT 'free'::text NOT NULL,
    monthly_usd_cap_override numeric(10,4),
    cycle_anchor_day smallint NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_quotas_cycle_anchor_day_check CHECK (((cycle_anchor_day >= 1) AND (cycle_anchor_day <= 31))),
    CONSTRAINT user_quotas_monthly_usd_cap_override_check CHECK (((monthly_usd_cap_override IS NULL) OR (monthly_usd_cap_override >= (0)::numeric)))
);


--
-- Name: usage_quota_periods; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_quota_periods (
    user_id uuid NOT NULL,
    cycle_start_at timestamp with time zone NOT NULL,
    cycle_end_at timestamp with time zone NOT NULL,
    used_usd numeric(12,6) DEFAULT 0 NOT NULL,
    last_event_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT usage_quota_periods_cycle_check CHECK ((cycle_end_at > cycle_start_at)),
    CONSTRAINT usage_quota_periods_used_usd_check CHECK ((used_usd >= (0)::numeric))
);


--
-- Name: background_jobs_dlq background_jobs_dlq_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.background_jobs_dlq
    ADD CONSTRAINT background_jobs_dlq_pkey PRIMARY KEY (id);


--
-- Name: background_jobs background_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.background_jobs
    ADD CONSTRAINT background_jobs_pkey PRIMARY KEY (id);


--
-- Name: brief_deliveries brief_deliveries_meeting_id_channel_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brief_deliveries
    ADD CONSTRAINT brief_deliveries_meeting_id_channel_key UNIQUE (meeting_id, channel);


--
-- Name: brief_deliveries brief_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brief_deliveries
    ADD CONSTRAINT brief_deliveries_pkey PRIMARY KEY (id);


--
-- Name: brief_feedback brief_feedback_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brief_feedback
    ADD CONSTRAINT brief_feedback_pkey PRIMARY KEY (id);


--
-- Name: brief_feedback brief_feedback_user_id_target_meeting_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brief_feedback
    ADD CONSTRAINT brief_feedback_user_id_target_meeting_id_key UNIQUE (user_id, target_meeting_id);


--
-- Name: calendar_subscriptions calendar_subscriptions_channel_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.calendar_subscriptions
    ADD CONSTRAINT calendar_subscriptions_channel_id_key UNIQUE (channel_id);


--
-- Name: calendar_subscriptions calendar_subscriptions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.calendar_subscriptions
    ADD CONSTRAINT calendar_subscriptions_pkey PRIMARY KEY (id);


--
-- Name: chain_decision_centroids chain_decision_centroids_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chain_decision_centroids
    ADD CONSTRAINT chain_decision_centroids_pkey PRIMARY KEY (chain_id);


--
-- Name: chain_decisions_v2 chain_decisions_v2_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chain_decisions_v2
    ADD CONSTRAINT chain_decisions_v2_pkey PRIMARY KEY (id);


--
-- Name: chain_decisions_v2 chain_decisions_v2_user_id_meeting_id_candidate_key_algorithm_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chain_decisions_v2
    ADD CONSTRAINT chain_decisions_v2_user_id_meeting_id_candidate_key_algorithm_version_key UNIQUE (user_id, meeting_id, candidate_key, algorithm_version);


--
-- Name: correction_dictionary correction_dictionary_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.correction_dictionary
    ADD CONSTRAINT correction_dictionary_pkey PRIMARY KEY (id);


--
-- Name: decision_items decision_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.decision_items
    ADD CONSTRAINT decision_items_pkey PRIMARY KEY (id);


--
-- Name: desktop_command_log desktop_command_log_meeting_id_command_type_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_command_log
    ADD CONSTRAINT desktop_command_log_meeting_id_command_type_key UNIQUE (meeting_id, command_type);


--
-- Name: desktop_command_log desktop_command_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_command_log
    ADD CONSTRAINT desktop_command_log_pkey PRIMARY KEY (id);


--
-- Name: desktop_sessions desktop_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_sessions
    ADD CONSTRAINT desktop_sessions_pkey PRIMARY KEY (id);


--
-- Name: meeting_briefs meeting_briefs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_briefs
    ADD CONSTRAINT meeting_briefs_pkey PRIMARY KEY (id);


--
-- Name: meeting_briefs meeting_briefs_user_id_target_meeting_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_briefs
    ADD CONSTRAINT meeting_briefs_user_id_target_meeting_id_key UNIQUE (user_id, target_meeting_id);


--
-- Name: meeting_chains meeting_chains_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_chains
    ADD CONSTRAINT meeting_chains_pkey PRIMARY KEY (id);


--
-- Name: meeting_notes meeting_notes_meeting_id_user_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_notes
    ADD CONSTRAINT meeting_notes_meeting_id_user_id_key UNIQUE (meeting_id, user_id);


--
-- Name: meeting_notes meeting_notes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_notes
    ADD CONSTRAINT meeting_notes_pkey PRIMARY KEY (id);


--
-- Name: meeting_summaries meeting_summaries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_summaries
    ADD CONSTRAINT meeting_summaries_pkey PRIMARY KEY (id);


--
-- Name: meetings meetings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meetings
    ADD CONSTRAINT meetings_pkey PRIMARY KEY (id);


--
-- Name: password_reset_tokens password_reset_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_reset_tokens
    ADD CONSTRAINT password_reset_tokens_pkey PRIMARY KEY (token);


--
-- Name: profiles profiles_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.profiles
    ADD CONSTRAINT profiles_email_key UNIQUE (email);


--
-- Name: profiles profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.profiles
    ADD CONSTRAINT profiles_pkey PRIMARY KEY (id);


--
-- Name: refresh_tokens refresh_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refresh_tokens
    ADD CONSTRAINT refresh_tokens_pkey PRIMARY KEY (id);


--
-- Name: refresh_tokens refresh_tokens_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refresh_tokens
    ADD CONSTRAINT refresh_tokens_token_key UNIQUE (token);


--
-- Name: slack_integrations slack_integrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.slack_integrations
    ADD CONSTRAINT slack_integrations_pkey PRIMARY KEY (id);


--
-- Name: slack_integrations slack_integrations_user_id_team_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.slack_integrations
    ADD CONSTRAINT slack_integrations_user_id_team_id_key UNIQUE (user_id, team_id);


--
-- Name: summary_errors summary_errors_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.summary_errors
    ADD CONSTRAINT summary_errors_pkey PRIMARY KEY (id);


--
-- Name: transcript_chat_blocks transcript_chat_blocks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transcript_chat_blocks
    ADD CONSTRAINT transcript_chat_blocks_pkey PRIMARY KEY (id);


--
-- Name: transcript_segments transcript_segments_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transcript_segments
    ADD CONSTRAINT transcript_segments_idempotency_key_key UNIQUE (idempotency_key);


--
-- Name: transcript_segments transcript_segments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transcript_segments
    ADD CONSTRAINT transcript_segments_pkey PRIMARY KEY (id);


--
-- Name: upload_sessions upload_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_sessions
    ADD CONSTRAINT upload_sessions_pkey PRIMARY KEY (id);


--
-- Name: usage_logs usage_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_logs
    ADD CONSTRAINT usage_logs_pkey PRIMARY KEY (id);


--
-- Name: users users_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_email_key UNIQUE (email);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: user_quota_events user_quota_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_quota_events
    ADD CONSTRAINT user_quota_events_pkey PRIMARY KEY (id);


--
-- Name: user_quotas user_quotas_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_quotas
    ADD CONSTRAINT user_quotas_pkey PRIMARY KEY (user_id);


--
-- Name: usage_quota_periods usage_quota_periods_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_quota_periods
    ADD CONSTRAINT usage_quota_periods_pkey PRIMARY KEY (user_id, cycle_start_at);


--
-- Name: idx_background_jobs_dlq_failed_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_background_jobs_dlq_failed_at ON public.background_jobs_dlq USING btree (failed_at DESC);


--
-- Name: idx_background_jobs_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_background_jobs_pending ON public.background_jobs USING btree (status, scheduled_at, created_at) WHERE (status = 'pending'::text);


--
-- Name: idx_background_jobs_type_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_background_jobs_type_status ON public.background_jobs USING btree (job_type, status, created_at DESC);


--
-- Name: idx_brief_deliveries_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_brief_deliveries_pending ON public.brief_deliveries USING btree (scheduled_for) WHERE (status = 'pending'::text);


--
-- Name: idx_calendar_subscriptions_channel_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_calendar_subscriptions_channel_id ON public.calendar_subscriptions USING btree (channel_id);


--
-- Name: idx_calendar_subscriptions_expiration; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_calendar_subscriptions_expiration ON public.calendar_subscriptions USING btree (expiration);


--
-- Name: idx_calendar_subscriptions_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_calendar_subscriptions_user_id ON public.calendar_subscriptions USING btree (user_id);


--
-- Name: idx_chain_decision_centroids_computed_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chain_decision_centroids_computed_at ON public.chain_decision_centroids USING btree (computed_at DESC);


--
-- Name: idx_chain_decisions_v2_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chain_decisions_v2_pending ON public.chain_decisions_v2 USING btree (user_id, created_at) WHERE (status = 'pending'::text);


--
-- Name: idx_chain_decisions_v2_shadow; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chain_decisions_v2_shadow ON public.chain_decisions_v2 USING btree (user_id, meeting_id, created_at) WHERE (mode = 'shadow'::text);


--
-- Name: idx_chain_decisions_v2_suppression; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chain_decisions_v2_suppression ON public.chain_decisions_v2 USING btree (user_id, suppression_key, status, created_at) WHERE (suppression_key IS NOT NULL);


--
-- Name: idx_chat_blocks_embedding; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chat_blocks_embedding ON public.transcript_chat_blocks USING hnsw (embedding public.vector_cosine_ops) WITH (m='16', ef_construction='64');


--
-- Name: idx_chat_blocks_embedding_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chat_blocks_embedding_status ON public.transcript_chat_blocks USING btree (embedding_generated_at) WHERE (embedding IS NOT NULL);


--
-- Name: idx_chat_blocks_finalized; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chat_blocks_finalized ON public.transcript_chat_blocks USING btree (meeting_id, is_finalized, start_time);


--
-- Name: idx_chat_blocks_meeting_start; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chat_blocks_meeting_start ON public.transcript_chat_blocks USING btree (meeting_id, start_time);


--
-- Name: idx_chat_blocks_not_noise; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_chat_blocks_not_noise ON public.transcript_chat_blocks USING btree (meeting_id, is_noise) WHERE (is_noise = false);


--
-- Name: idx_correction_dictionary_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_correction_dictionary_active ON public.correction_dictionary USING btree (is_active) WHERE (is_active = true);


--
-- Name: idx_decision_items_chain_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_decision_items_chain_status ON public.decision_items USING btree (chain_id, status);


--
-- Name: idx_decision_items_meeting; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_decision_items_meeting ON public.decision_items USING btree (meeting_id);


--
-- Name: idx_decision_items_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_decision_items_type ON public.decision_items USING btree (item_type, status);


--
-- Name: idx_decision_items_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_decision_items_user ON public.decision_items USING btree (user_id);


--
-- Name: idx_desktop_command_log_meeting; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_desktop_command_log_meeting ON public.desktop_command_log USING btree (meeting_id);


--
-- Name: idx_desktop_command_log_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_desktop_command_log_user ON public.desktop_command_log USING btree (user_id);


--
-- Name: idx_desktop_sessions_user_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_desktop_sessions_user_active ON public.desktop_sessions USING btree (user_id, disconnected_at);


--
-- Name: idx_meeting_briefs_target; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meeting_briefs_target ON public.meeting_briefs USING btree (target_meeting_id);


--
-- Name: idx_meeting_briefs_user_meeting; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meeting_briefs_user_meeting ON public.meeting_briefs USING btree (user_id, target_meeting_id);


--
-- Name: idx_meeting_chains_series; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meeting_chains_series ON public.meeting_chains USING btree (calendar_series_id) WHERE (calendar_series_id IS NOT NULL);


--
-- Name: idx_meeting_chains_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meeting_chains_user ON public.meeting_chains USING btree (user_id);


--
-- Name: idx_meeting_chains_user_series_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_meeting_chains_user_series_unique ON public.meeting_chains USING btree (user_id, calendar_series_id) WHERE (calendar_series_id IS NOT NULL);


--
-- Name: idx_meeting_chains_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meeting_chains_type ON public.meeting_chains USING btree (chain_type);


--
-- Name: idx_meeting_notes_meeting_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meeting_notes_meeting_user ON public.meeting_notes USING btree (meeting_id, user_id);


--
-- Name: idx_meeting_summaries_meeting_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meeting_summaries_meeting_id ON public.meeting_summaries USING btree (meeting_id);


--
-- Name: idx_meeting_summaries_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meeting_summaries_source ON public.meeting_summaries USING btree (summary_source, created_at DESC) WHERE (summary_type = 'final_detailed'::text);


--
-- Name: idx_meeting_summaries_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meeting_summaries_status ON public.meeting_summaries USING btree (meeting_id, summary_type, processing_status);


--
-- Name: idx_meeting_summaries_unique_type; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_meeting_summaries_unique_type ON public.meeting_summaries USING btree (meeting_id, summary_type);


--
-- Name: idx_meetings_audio_cleanup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_audio_cleanup ON public.meetings USING btree (transcription_status, transcription_completed_at) WHERE ((audio_storage_path IS NOT NULL) AND (audio_deleted_at IS NULL));


--
-- Name: idx_meetings_auto_bot_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_auto_bot_enabled ON public.meetings USING btree (auto_bot_enabled) WHERE (auto_bot_enabled = true);


--
-- Name: idx_meetings_bot_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_bot_status ON public.meetings USING btree (bot_status) WHERE (bot_status IS NOT NULL);


--
-- Name: idx_meetings_bot_watchdog; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_bot_watchdog ON public.meetings USING btree (bot_status, last_heartbeat_at);


--
-- Name: idx_meetings_calendar_series; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_calendar_series ON public.meetings USING btree (calendar_series_id) WHERE (calendar_series_id IS NOT NULL);


--
-- Name: idx_meetings_chain; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_chain ON public.meetings USING btree (chain_id) WHERE (chain_id IS NOT NULL);


--
-- Name: idx_meetings_google_event_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_google_event_id ON public.meetings USING btree (google_event_id);


--
-- Name: idx_meetings_meeting_url; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_meeting_url ON public.meetings USING btree (meeting_url) WHERE (meeting_url IS NOT NULL);


--
-- Name: idx_meetings_processed; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_processed ON public.meetings USING btree (processed_at) WHERE (processed_at IS NOT NULL);


--
-- Name: idx_meetings_start_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_start_time ON public.meetings USING btree (start_time);


--
-- Name: idx_meetings_transcription_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_transcription_status ON public.meetings USING btree (transcription_status) WHERE (transcription_status = ANY (ARRAY['pending'::text, 'uploading'::text, 'processing'::text, 'failed'::text]));


--
-- Name: idx_meetings_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_meetings_user_id ON public.meetings USING btree (user_id);


--
-- Name: idx_password_reset_tokens_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_password_reset_tokens_user_id ON public.password_reset_tokens USING btree (user_id);


--
-- Name: idx_profiles_calendar_connected; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_profiles_calendar_connected ON public.profiles USING btree (is_calendar_connected) WHERE (is_calendar_connected = true);


--
-- Name: idx_profiles_google_email; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_profiles_google_email ON public.profiles USING btree (google_email) WHERE (google_email IS NOT NULL);


--
-- Name: idx_profiles_google_refresh_token; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_profiles_google_refresh_token ON public.profiles USING btree (google_refresh_token) WHERE (google_refresh_token IS NOT NULL);


--
-- Name: idx_refresh_tokens_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_refresh_tokens_expires_at ON public.refresh_tokens USING btree (expires_at);


--
-- Name: idx_refresh_tokens_revoked; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_refresh_tokens_revoked ON public.refresh_tokens USING btree (revoked) WHERE (revoked = false);


--
-- Name: idx_refresh_tokens_token; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_refresh_tokens_token ON public.refresh_tokens USING btree (token);


--
-- Name: idx_refresh_tokens_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_refresh_tokens_user_id ON public.refresh_tokens USING btree (user_id);


--
-- Name: idx_slack_integrations_user_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_slack_integrations_user_active ON public.slack_integrations USING btree (user_id) WHERE (revoked_at IS NULL);


--
-- Name: idx_summary_errors_meeting; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_summary_errors_meeting ON public.summary_errors USING btree (meeting_id);


--
-- Name: idx_summary_errors_unresolved; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_summary_errors_unresolved ON public.summary_errors USING btree (meeting_id, resolved) WHERE (resolved = false);


--
-- Name: idx_transcript_segments_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transcript_segments_created_at ON public.transcript_segments USING btree (created_at DESC);


--
-- Name: idx_transcript_segments_idempotency; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transcript_segments_idempotency ON public.transcript_segments USING btree (idempotency_key) WHERE (idempotency_key IS NOT NULL);


--
-- Name: idx_transcript_segments_meeting_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transcript_segments_meeting_id ON public.transcript_segments USING btree (meeting_id);


--
-- Name: idx_transcript_segments_speaker; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transcript_segments_speaker ON public.transcript_segments USING btree (speaker) WHERE (speaker IS NOT NULL);


--
-- Name: idx_transcript_segments_start_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transcript_segments_start_time ON public.transcript_segments USING btree (start_time);


--
-- Name: idx_upload_sessions_meeting; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_upload_sessions_meeting ON public.upload_sessions USING btree (meeting_id);


--
-- Name: idx_upload_sessions_user_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_upload_sessions_user_status ON public.upload_sessions USING btree (user_id, status) WHERE (status = 'in_progress'::text);


--
-- Name: idx_usage_logs_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_logs_created_at ON public.usage_logs USING btree (created_at);


--
-- Name: idx_usage_logs_feature; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_logs_feature ON public.usage_logs USING btree (feature);


--
-- Name: idx_usage_logs_meeting_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_logs_meeting_id ON public.usage_logs USING btree (meeting_id);


--
-- Name: idx_usage_logs_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_logs_user_id ON public.usage_logs USING btree (user_id);


--
-- Name: idx_usage_logs_user_month; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_logs_user_month ON public.usage_logs USING btree (user_id, created_at);


--
-- Name: idx_user_quota_events_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_quota_events_user ON public.user_quota_events USING btree (user_id, created_at DESC);


--
-- Name: idx_user_quotas_tier; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_quotas_tier ON public.user_quotas USING btree (tier) WHERE (tier <> 'free'::text);


--
-- Name: idx_usage_quota_periods_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_quota_periods_active ON public.usage_quota_periods USING btree (user_id, cycle_end_at DESC);


--
-- Name: idx_users_email; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_email ON public.users USING btree (email);


--
-- Name: idx_users_is_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_is_active ON public.users USING btree (is_active) WHERE (is_active = true);


--
-- Name: calendar_subscriptions calendar_subscriptions_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER calendar_subscriptions_updated_at BEFORE UPDATE ON public.calendar_subscriptions FOR EACH ROW EXECUTE FUNCTION public.update_calendar_subscriptions_updated_at();


--
-- Name: profiles on_refresh_token_change; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER on_refresh_token_change BEFORE UPDATE OF google_refresh_token ON public.profiles FOR EACH ROW EXECUTE FUNCTION public.update_calendar_connection_status();


--
-- Name: user_quotas user_quotas_set_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER user_quotas_set_updated_at BEFORE UPDATE ON public.user_quotas FOR EACH ROW EXECUTE FUNCTION public.update_user_quotas_updated_at();


--
-- Name: users on_user_created; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER on_user_created AFTER INSERT ON public.users FOR EACH ROW EXECUTE FUNCTION public.handle_new_user();


--
-- Name: users users_create_quota; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER users_create_quota AFTER INSERT ON public.users FOR EACH ROW EXECUTE FUNCTION public.create_user_quota_on_signup();


--
-- Name: users update_users_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_users_updated_at BEFORE UPDATE ON public.users FOR EACH ROW EXECUTE FUNCTION public.update_users_updated_at();


--
-- Name: upload_sessions upload_sessions_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER upload_sessions_updated_at BEFORE UPDATE ON public.upload_sessions FOR EACH ROW EXECUTE FUNCTION public.upload_sessions_set_updated_at();


--
-- Name: background_jobs_dlq background_jobs_dlq_original_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.background_jobs_dlq
    ADD CONSTRAINT background_jobs_dlq_original_job_id_fkey FOREIGN KEY (original_job_id) REFERENCES public.background_jobs(id) ON DELETE SET NULL;


--
-- Name: brief_deliveries brief_deliveries_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brief_deliveries
    ADD CONSTRAINT brief_deliveries_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.meetings(id) ON DELETE CASCADE;


--
-- Name: brief_deliveries brief_deliveries_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brief_deliveries
    ADD CONSTRAINT brief_deliveries_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: brief_feedback brief_feedback_brief_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brief_feedback
    ADD CONSTRAINT brief_feedback_brief_id_fkey FOREIGN KEY (brief_id) REFERENCES public.meeting_briefs(id);


--
-- Name: brief_feedback brief_feedback_target_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brief_feedback
    ADD CONSTRAINT brief_feedback_target_meeting_id_fkey FOREIGN KEY (target_meeting_id) REFERENCES public.meetings(id);


--
-- Name: brief_feedback brief_feedback_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brief_feedback
    ADD CONSTRAINT brief_feedback_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: calendar_subscriptions calendar_subscriptions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.calendar_subscriptions
    ADD CONSTRAINT calendar_subscriptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: chain_decision_centroids chain_decision_centroids_chain_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chain_decision_centroids
    ADD CONSTRAINT chain_decision_centroids_chain_id_fkey FOREIGN KEY (chain_id) REFERENCES public.meeting_chains(id) ON DELETE CASCADE;


--
-- Name: chain_decisions_v2 chain_decisions_v2_candidate_chain_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chain_decisions_v2
    ADD CONSTRAINT chain_decisions_v2_candidate_chain_id_fkey FOREIGN KEY (candidate_chain_id) REFERENCES public.meeting_chains(id) ON DELETE SET NULL;


--
-- Name: chain_decisions_v2 chain_decisions_v2_matched_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chain_decisions_v2
    ADD CONSTRAINT chain_decisions_v2_matched_meeting_id_fkey FOREIGN KEY (matched_meeting_id) REFERENCES public.meetings(id) ON DELETE SET NULL;


--
-- Name: chain_decisions_v2 chain_decisions_v2_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chain_decisions_v2
    ADD CONSTRAINT chain_decisions_v2_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.meetings(id) ON DELETE CASCADE;


--
-- Name: chain_decisions_v2 chain_decisions_v2_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chain_decisions_v2
    ADD CONSTRAINT chain_decisions_v2_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: decision_items decision_items_chain_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.decision_items
    ADD CONSTRAINT decision_items_chain_id_fkey FOREIGN KEY (chain_id) REFERENCES public.meeting_chains(id);


--
-- Name: decision_items decision_items_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.decision_items
    ADD CONSTRAINT decision_items_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.meetings(id) ON DELETE CASCADE;


--
-- Name: decision_items decision_items_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.decision_items
    ADD CONSTRAINT decision_items_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: desktop_command_log desktop_command_log_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_command_log
    ADD CONSTRAINT desktop_command_log_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.meetings(id) ON DELETE CASCADE;


--
-- Name: desktop_command_log desktop_command_log_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_command_log
    ADD CONSTRAINT desktop_command_log_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: desktop_sessions desktop_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.desktop_sessions
    ADD CONSTRAINT desktop_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: meeting_briefs meeting_briefs_chain_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_briefs
    ADD CONSTRAINT meeting_briefs_chain_id_fkey FOREIGN KEY (chain_id) REFERENCES public.meeting_chains(id);


--
-- Name: meeting_briefs meeting_briefs_target_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_briefs
    ADD CONSTRAINT meeting_briefs_target_meeting_id_fkey FOREIGN KEY (target_meeting_id) REFERENCES public.meetings(id);


--
-- Name: meeting_briefs meeting_briefs_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_briefs
    ADD CONSTRAINT meeting_briefs_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: meeting_chains meeting_chains_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_chains
    ADD CONSTRAINT meeting_chains_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: meeting_notes meeting_notes_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_notes
    ADD CONSTRAINT meeting_notes_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.meetings(id) ON DELETE CASCADE;


--
-- Name: meeting_notes meeting_notes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_notes
    ADD CONSTRAINT meeting_notes_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: meeting_summaries meeting_summaries_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meeting_summaries
    ADD CONSTRAINT meeting_summaries_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.meetings(id) ON DELETE CASCADE;


--
-- Name: meetings meetings_chain_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meetings
    ADD CONSTRAINT meetings_chain_id_fkey FOREIGN KEY (chain_id) REFERENCES public.meeting_chains(id);


--
-- Name: meetings meetings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meetings
    ADD CONSTRAINT meetings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: password_reset_tokens password_reset_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_reset_tokens
    ADD CONSTRAINT password_reset_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: profiles profiles_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.profiles
    ADD CONSTRAINT profiles_id_fkey FOREIGN KEY (id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: refresh_tokens refresh_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refresh_tokens
    ADD CONSTRAINT refresh_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_quota_events user_quota_events_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_quota_events
    ADD CONSTRAINT user_quota_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_quotas user_quotas_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_quotas
    ADD CONSTRAINT user_quotas_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: usage_quota_periods usage_quota_periods_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_quota_periods
    ADD CONSTRAINT usage_quota_periods_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: slack_integrations slack_integrations_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.slack_integrations
    ADD CONSTRAINT slack_integrations_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: summary_errors summary_errors_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.summary_errors
    ADD CONSTRAINT summary_errors_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.meetings(id) ON DELETE CASCADE;


--
-- Name: transcript_chat_blocks transcript_chat_blocks_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transcript_chat_blocks
    ADD CONSTRAINT transcript_chat_blocks_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.meetings(id) ON DELETE CASCADE;


--
-- Name: transcript_segments transcript_segments_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transcript_segments
    ADD CONSTRAINT transcript_segments_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.meetings(id) ON DELETE SET NULL;


--
-- Name: upload_sessions upload_sessions_meeting_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_sessions
    ADD CONSTRAINT upload_sessions_meeting_id_fkey FOREIGN KEY (meeting_id) REFERENCES public.meetings(id) ON DELETE CASCADE;


--
-- Name: upload_sessions upload_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_sessions
    ADD CONSTRAINT upload_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: brief_deliveries; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.brief_deliveries ENABLE ROW LEVEL SECURITY;

--
-- Name: desktop_command_log; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.desktop_command_log ENABLE ROW LEVEL SECURITY;

--
-- Name: desktop_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.desktop_sessions ENABLE ROW LEVEL SECURITY;

--
-- Name: slack_integrations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.slack_integrations ENABLE ROW LEVEL SECURITY;

--
-- Name: upload_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.upload_sessions ENABLE ROW LEVEL SECURITY;

--
-- PostgreSQL database dump complete
--
