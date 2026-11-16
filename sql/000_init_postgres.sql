-- ============================================================
-- Standalone PostgreSQL 16 (+ pgvector) Initialization Script
-- Compatible with local Docker pgvector/pgvector:pg16 & GCP Cloud SQL
-- ============================================================

-- Generated from base schema + migrations (009, 010, 014-018, 020).
-- Migration 022 is intentionally excluded so new users default to
-- recording_method = 'bot' for the ElevenLabs Live API meeting bot.

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', 'public', false);
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

CREATE EXTENSION IF NOT EXISTS pgcrypto;
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

-- ALTER TABLE public.brief_deliveries ENABLE ROW LEVEL SECURITY;

--
-- Name: desktop_command_log; Type: ROW SECURITY; Schema: public; Owner: -
--

-- ALTER TABLE public.desktop_command_log ENABLE ROW LEVEL SECURITY;

--
-- Name: desktop_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

-- ALTER TABLE public.desktop_sessions ENABLE ROW LEVEL SECURITY;

--
-- Name: slack_integrations; Type: ROW SECURITY; Schema: public; Owner: -
--

-- ALTER TABLE public.slack_integrations ENABLE ROW LEVEL SECURITY;

--
-- Name: upload_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

-- ALTER TABLE public.upload_sessions ENABLE ROW LEVEL SECURITY;

--
-- PostgreSQL database dump complete
--

SET search_path TO public;

-- >>> Migration: 009_meetings_unique_index_nonpartial.sql
-- Migration 009 — Drop partial unique index, recreate as non-partial.
--
-- Migration 008 created a PARTIAL unique index on
-- meetings (user_id, google_event_id) WHERE google_event_id IS NOT NULL
-- AND google_event_id <> ''. PostgreSQL requires the inference predicate
-- on `INSERT ... ON CONFLICT (cols)` to match the partial index's WHERE
-- clause exactly; since the Go upsert in MeetingRepository.UpsertDesktopMeeting
-- uses the bare `ON CONFLICT (user_id, google_event_id)` form, every call
-- aborts with `there is no unique or exclusion constraint matching the
-- ON CONFLICT specification (SQLSTATE 42P10)`, manifesting in production
-- as `POST /api/meetings/desktop -> 500 Toplantı kaydedilemedi`.
--
-- The partial filter from 008 was defensive — `meetings.google_event_id`
-- is `TEXT NOT NULL` in the schema (sql/001_mvp_schema.sql:138), so the
-- predicate cannot exclude any row. Replacing the index with a plain
-- (non-partial) unique index makes the existing ON CONFLICT clause
-- match without code changes.
--
-- Idempotent: DROP IF EXISTS + CREATE IF NOT EXISTS on a different name
-- (so re-running this migration after an interrupted run still converges).

BEGIN;

DROP INDEX IF EXISTS public.meetings_user_google_event_unique;

CREATE UNIQUE INDEX IF NOT EXISTS meetings_user_google_event_unique
    ON public.meetings (user_id, google_event_id);

COMMIT;


-- >>> Migration: 010_transcript_segments_role_default.sql
-- Migration 010 — Make transcript_segments.role optional with default ''.
--
-- The `role` column is a holdover from the meeting-bot transcript path,
-- where each line was tagged "host"/"guest"/etc. The Phase 8 batch path
-- (ElevenLabs Scribe) writes word-level segments via
-- TranscriptRepository.InsertSegments which doesn't carry a role — and
-- Scribe doesn't expose speaker tagging on the single-channel codepath
-- we use. Without this migration the INSERT fails with
-- `null value in column "role" of relation "transcript_segments"
-- violates not-null constraint (SQLSTATE 23502)`, leaving every
-- transcribed meeting stuck at transcription_status=processing with
-- zero segments.
--
-- The same ALTER was already executed against the production Supabase
-- DB during the Phase 1 E2E acceptance pass; this file commits it to
-- the repo so dev / CI / future re-deploys converge on the same schema.
--
-- Idempotent — safe to re-run; ALTER COLUMN is a no-op when the column
-- already matches the requested shape.

BEGIN;

ALTER TABLE public.transcript_segments
    ALTER COLUMN role SET DEFAULT '',
    ALTER COLUMN role DROP NOT NULL;

COMMIT;


-- >>> Migration: 014_profiles_teams_onboarding_status.sql
-- Migration 014 — Phase 4 v1.1 (Team Detection & Onboarding)
--
-- Adds `profiles.teams_onboarding_status` — a TEXT enum-like column that
-- tracks where each user sits in the v1.1 team-detection onboarding
-- funnel. Allowed values per Phase 4 CONTEXT.md D-04:
--   pending             — initial state, user has not completed onboarding
--   completed           — detection ran, user saved teams (or already
--                         had a calendar connected pre-v1.1, see backfill)
--   skipped             — user explicitly dismissed the onboarding
--   empty_state_skipped — algorithm found zero teams, user accepted
--
-- Default is 'pending' (NOT NULL). The column is enforced by a CHECK
-- constraint named `profiles_teams_onboarding_status_check` so the four
-- allowed values are guarded at the DB layer (mirrors the migration 013
-- TEXT+CHECK pattern instead of a Postgres ENUM type).
--
-- Inline backfill (atomic with the ALTER inside BEGIN/COMMIT): every
-- profile that already has `is_calendar_connected = TRUE` is moved to
-- 'completed' so a re-auth, page refresh, or token refresh on a
-- pre-existing connected user does NOT retroactively trigger the
-- onboarding flow (structural prevention for pitfall P10). Users with
-- `is_calendar_connected = FALSE` (or NULL) keep the default 'pending'
-- and will go through onboarding the first time they connect.
--
-- Idempotent:
--   * `ADD COLUMN IF NOT EXISTS` is safe on re-run.
--   * `DROP CONSTRAINT IF EXISTS` + `ADD CONSTRAINT` re-creates the CHECK
--     cleanly so the migration tolerates an existing partial state.
--   * The backfill UPDATE includes `AND teams_onboarding_status = 'pending'`
--     — re-running the migration will NOT regress users who have
--     legitimately advanced to 'skipped' or 'empty_state_skipped' back
--     to 'completed'.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

ALTER TABLE public.profiles
    ADD COLUMN IF NOT EXISTS teams_onboarding_status TEXT NOT NULL DEFAULT 'pending';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'profiles'
          AND constraint_name = 'profiles_teams_onboarding_status_check'
    ) THEN
        ALTER TABLE public.profiles
            DROP CONSTRAINT profiles_teams_onboarding_status_check;
    END IF;

    ALTER TABLE public.profiles
        ADD CONSTRAINT profiles_teams_onboarding_status_check
        CHECK (
            teams_onboarding_status IN (
                'pending',
                'completed',
                'skipped',
                'empty_state_skipped'
            )
        );
END $$;

-- Backfill: pre-existing connected users land directly on 'completed' so
-- re-auth, page refresh, or token refresh cannot trigger onboarding.
-- Idempotent: only touches rows still at the default 'pending'.
UPDATE public.profiles
SET teams_onboarding_status = 'completed'
WHERE is_calendar_connected = TRUE
  AND teams_onboarding_status = 'pending';

COMMIT;


-- >>> Migration: 015_teams.sql
-- Migration 015 — Phase 4 v1.1 (Team Detection & Onboarding)
--
-- Creates `public.teams` — the user-scoped team table that holds the
-- output of the v1.1 onboarding team-detection flow (and any manual
-- create/edit/merge operations the user performs from the edit page).
--
-- Schema contract for v1.1 (Phase 4 CONTEXT.md D-01/D-02 + REQUIREMENTS
-- TEAMS-01):
--   id          UUID PK, server-generated
--   user_id     UUID FK → profiles(id) ON DELETE CASCADE — owner scope
--   name        TEXT NOT NULL — display label, user-editable
--   created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
--   updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
--
-- UNIQUE (user_id, name) — same user cannot have two teams sharing a
-- name; different users may legitimately reuse names. Enforced at the
-- DB layer with a NAMED constraint (`teams_user_id_name_unique`) so
-- repository unit tests in plan 04-04 can detect the violation by
-- constraint name and translate it deterministically (e.g., to a 409).
--
-- ON DELETE CASCADE on user_id — deleting a profile hard-deletes the
-- user's teams (which then cascades into team_members via migration
-- 016). This realises PRIVACY-03's "delete teams ⇒ delete members"
-- spirit at the FK layer.
--
-- `gen_random_uuid()` is available in Supabase by default (pgcrypto
-- preloaded), so no `CREATE EXTENSION` line is needed — matches the
-- prevailing project pattern.
--
-- Updated_at is set explicitly by the repository layer (no DB trigger)
-- — matches existing pattern in `meetings`, `meeting_summaries`.
--
-- Idempotent: `CREATE TABLE IF NOT EXISTS` + `CREATE INDEX IF NOT EXISTS`.
-- Re-running the migration on a database that already has the table is
-- a no-op.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

CREATE TABLE IF NOT EXISTS public.teams (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT teams_user_id_name_unique UNIQUE (user_id, name)
);

CREATE INDEX IF NOT EXISTS idx_teams_user_id ON public.teams (user_id);

COMMIT;


-- >>> Migration: 016_team_members.sql
-- Migration 016 — Phase 4 v1.1 (Team Detection & Onboarding)
--
-- Creates `public.team_members` — the per-team membership table that
-- holds the strictly-minimal PII set chosen for v1.1 (REQUIREMENTS
-- TEAMS-02 + PRIVACY-04 + research pitfall P4 — KVKK / shadow contact
-- DB).
--
-- Schema contract (seven columns, no others):
--   id          UUID PK, server-generated
--   team_id     UUID FK → teams(id) ON DELETE CASCADE
--   name        TEXT NOT NULL — display name (from calendar attendee)
--   title       TEXT NULL    — user-typed on edit page (EDIT-08 says
--                              "field is optional and empty by default")
--   email       TEXT NOT NULL — deduplication key
--   created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
--   updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
--
-- UNIQUE (team_id, email) — the same email may legitimately appear
-- across different teams of the same user (DETECT-01 allows
-- overlapping team membership), but never twice within one team.
-- Enforced via the NAMED constraint `team_members_team_id_email_unique`
-- so repository tests can detect the violation deterministically by
-- constraint name.
--
-- ON DELETE CASCADE on team_id realises PRIVACY-03 ("deleting a team
-- hard-deletes its members") at the database layer. Repository tests
-- in plan 04-04 verify this contract.
--
-- Privacy contract (PRIVACY-04) — PERMANENT, NOT A DEFERRAL:
--   Phone, photo, manager-of, LinkedIn-style fields are explicitly
--   out of scope. They cannot leak because they cannot be stored.
--   Adding any of these requires a milestone-level decision, not a
--   future migration follow-up. This schema is a hard ceiling, not
--   a starting point.
--
-- Idempotent: `CREATE TABLE IF NOT EXISTS` + `CREATE INDEX IF NOT EXISTS`.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

CREATE TABLE IF NOT EXISTS public.team_members (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL REFERENCES public.teams(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    title TEXT NULL,
    email TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT team_members_team_id_email_unique UNIQUE (team_id, email)
);

CREATE INDEX IF NOT EXISTS idx_team_members_team_id ON public.team_members (team_id);

COMMIT;


-- >>> Migration: 017_team_detection_runs.sql
-- Migration 017 — Phase 4 v1.1 (Team Detection & Onboarding)
--
-- Creates `public.team_detection_runs` — the run-reproducibility log
-- chosen as the structural prevention for pitfall P12 ("no production
-- reproducibility"). The table lands on day one even though the v1.1
-- algorithm only writes 1–2 rows per user lifetime (first-connect-only
-- trigger). Replay/observability tooling that reads the rows is
-- explicitly deferred to v1.2 (RESOLVED TENSION D — runs IN, replay OUT).
--
-- Schema (twelve columns, lean reproducibility set per Phase 4
-- CONTEXT.md D-08, in this exact order):
--   id                    UUID PK, server-generated
--   user_id               UUID FK → profiles(id) ON DELETE CASCADE
--   started_at            TIMESTAMPTZ NOT NULL — set when run begins
--   finished_at           TIMESTAMPTZ NULL    — NULL while running
--   status                TEXT NOT NULL       — see CHECK below
--   input_meeting_count   INTEGER NULL        — fetcher window summary
--   input_attendee_count  INTEGER NULL        — distinct attendees
--   output_team_count     INTEGER NULL        — clusters produced
--   parameters_jsonb      JSONB NULL          — algorithm parameter
--                                               snapshot owned by Phase 6
--                                               (k, weight-decay, cutoffs);
--                                               opaque to Phase 4
--   algorithm_version     TEXT NULL           — short tag like "cpm-k3-v1";
--                                               value space owned by Phase 6
--   error_message         TEXT NULL
--   created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
--
-- Status lifecycle (D-08):
--   running     — row inserted at run start, finished_at still NULL
--   success     — algorithm produced ≥1 team, output_team_count > 0
--   degenerate  — algorithm completed but produced 0 teams
--                 (powers the empty-state UX)
--   error       — algorithm crashed, error_message populated
--
-- Enforced via NAMED constraint `team_detection_runs_status_check`
-- using the DROP/ADD pattern (mirrors migration 013) so the value list
-- can expand cleanly in a future migration.
--
-- Index `idx_team_detection_runs_user_started` on (user_id,
-- started_at DESC) supports Phase 7's "latest run for user" lookup
-- (trust bundle DF-1).
--
-- No retention/cleanup logic in v1.1 (D-10): first-connect-only trigger
-- means at most 1–2 rows per user; volume is trivial. Pruning is
-- deferred until/unless replay tooling lands.
--
-- Phase 4 only lands the schema. Phase 6 owns the values that go into
-- `parameters_jsonb` and `algorithm_version` — Phase 4 treats them as
-- opaque blobs. There is intentionally no `version` column for the
-- JSONB blob (versioning belongs inside the JSON payload if Phase 6
-- needs it). There are no per-stage timing columns (fetch_ms, etc.) —
-- explicitly deferred. There is no `confidence` column — DF-2 is a
-- Phase 7 P1.5 ship-if-capacity item; if it ships, Phase 7 adds the
-- column then.
--
-- Idempotent: `CREATE TABLE IF NOT EXISTS` + DROP/ADD CHECK pattern +
-- `CREATE INDEX IF NOT EXISTS`.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

CREATE TABLE IF NOT EXISTS public.team_detection_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NULL,
    status TEXT NOT NULL,
    input_meeting_count INTEGER NULL,
    input_attendee_count INTEGER NULL,
    output_team_count INTEGER NULL,
    parameters_jsonb JSONB NULL,
    algorithm_version TEXT NULL,
    error_message TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'team_detection_runs'
          AND constraint_name = 'team_detection_runs_status_check'
    ) THEN
        ALTER TABLE public.team_detection_runs
            DROP CONSTRAINT team_detection_runs_status_check;
    END IF;

    ALTER TABLE public.team_detection_runs
        ADD CONSTRAINT team_detection_runs_status_check
        CHECK (
            status IN ('running', 'success', 'degenerate', 'error')
        );
END $$;

CREATE INDEX IF NOT EXISTS idx_team_detection_runs_user_started
    ON public.team_detection_runs (user_id, started_at DESC);

COMMIT;


-- >>> Migration: 018_team_detection_runs_full_metrics.sql
-- Migration 018 — Phase 6 v1.1 gap closure (OBS-01 full coverage)
--
-- Extends public.team_detection_runs with four additional metric
-- columns to satisfy ROADMAP Phase 6 success criterion #4 literally:
-- "input metrics (meeting/attendee/edge count), output metrics
-- (team/member/largest-team count), per-stage duration breakdown".
--
-- This REVERSES Phase 4 migration 017's deferral comment ("There are
-- no per-stage timing columns ... explicitly deferred"). The
-- verifier (gsd-verifier 2026-05-11) flagged the row as missing
-- input_edge_count, output_member_count, output_largest_team_size,
-- and per-stage durations on a literal reading of the must-have.
-- The user chose the full schema-extension resolution over the
-- alternative (amend ROADMAP wording to point at Prometheus).
--
-- New columns (all NULL-able; existing rows are not backfilled —
-- they predate this column set and remain valid as historical
-- run records with NULL in the new columns):
--   input_edge_count          INTEGER NULL  — g.Edges().Len() after BuildGraph
--   output_member_count       INTEGER NULL  — distinct emails union across all DetectedTeam.Members
--   output_largest_team_size  INTEGER NULL  — max(len(team.Members)) across the returned slice
--   stage_durations_jsonb     JSONB NULL    — {"fetch":<sec>,"graph_build":<sec>,"cluster":<sec>,"score":<sec>}
--                                            — keys verbatim match metrics.Stage* constants;
--                                              persist stage NOT included (self-referential — see service.go doc)
--
-- Idempotent: ADD COLUMN IF NOT EXISTS pattern. Safe to re-run.
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.
-- No retention/cleanup logic — see migration 017 D-10 rationale.

BEGIN;

ALTER TABLE public.team_detection_runs
    ADD COLUMN IF NOT EXISTS input_edge_count INTEGER NULL;

ALTER TABLE public.team_detection_runs
    ADD COLUMN IF NOT EXISTS output_member_count INTEGER NULL;

ALTER TABLE public.team_detection_runs
    ADD COLUMN IF NOT EXISTS output_largest_team_size INTEGER NULL;

ALTER TABLE public.team_detection_runs
    ADD COLUMN IF NOT EXISTS stage_durations_jsonb JSONB NULL;

COMMIT;


-- >>> Migration: 020_user_consents.sql
-- Migration 020: user_consents — append-only consent audit log (Phase 1.1)
-- Roadmap: issue #41 (Terms & Agreement / KVKK GDPR).
--
-- Records every consent action a user takes (acceptance, withdrawal,
-- re-acceptance after version bump). KVKK m.5/1 ispat yükümlülüğü:
-- veri sorumlusunun açık rızanın alındığını ispatlama yükümlülüğü
-- vardır; salt "users.terms_accepted_at" kolonu yeterli değildir
-- çünkü versiyon değişince geçmiş silinir. Bu yüzden append-only.
--
-- Design notes:
--   - One row per consent ACTION, not per user-type pair. New version
--     accepted → new row. Marketing withdrawn → new row with granted=false.
--   - "Active consent" = en son satır (accepted_at DESC) o user+type için
--     revoked_at IS NULL ve granted=true olan. App tarafında resolve edilir.
--   - event_group_id, aynı signup formundan gelen birden çok consent satırını
--     gruplar (Terms+Privacy+KVKK+AI+CrossBorder hep birlikte kabul edilir).
--   - document_version semantic ('1.0', '1.1' gibi) — app tarafında metin
--     dosyalarının başındaki "Sürüm: 1.0" satırıyla eşleşir.
--   - ip_address INET + user_agent TEXT denetimde kim/nereden onayladığını
--     kanıtlamak için saklanır. KVKK Kurul kararlarında bu bilgi istenmiştir.
--
-- consent_type enum (Phase 1):
--   terms_of_service       — Kullanım Koşulları (zorunlu)
--   privacy_policy         — Gizlilik Politikası (zorunlu)
--   kvkk_aydinlatma        — KVKK Aydınlatma Metni okundu beyanı (zorunlu)
--   cross_border_transfer  — Yurt dışı veri aktarımı açık rızası
--                            (KVKK m.9, zorunlu — servis çalışamaz değilse)
--   ai_processing          — Toplantı içeriklerinin AI ile işlenmesi açık rızası
--                            (KVKK m.5/1, zorunlu — temel işlev)
--   pilot_sozlesmesi       — Pilot Kullanıcı Sözleşmesi (pilot kullanıcılar için)
--   marketing              — Ticari elektronik ileti (opsiyonel, ayrı rıza)
--
-- Yeni consent_type eklemek için ayrı bir migration gerekir; CHECK constraint
-- bilinçli olarak whitelist ve enum yerine TEXT — Postgres enum'ları
-- migration zorlaştırır.

BEGIN;

SELECT pg_advisory_xact_lock(7400200);  -- unique migration id

CREATE TABLE IF NOT EXISTS public.user_consents (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    consent_type      TEXT NOT NULL CHECK (consent_type IN (
        'terms_of_service',
        'privacy_policy',
        'kvkk_aydinlatma',
        'cross_border_transfer',
        'ai_processing',
        'pilot_sozlesmesi',
        'marketing'
    )),
    document_version  TEXT NOT NULL,
    document_url      TEXT,
    granted           BOOLEAN NOT NULL DEFAULT TRUE,
    accepted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at        TIMESTAMPTZ,
    revoke_reason     TEXT,
    ip_address        INET,
    user_agent        TEXT,
    -- Same signup form → same event_group_id across rows. Nullable so
    -- programmatic single-consent updates (e.g. marketing withdrawal
    -- from settings) don't need to fabricate a group id.
    event_group_id    UUID,
    -- Free-form extension (locale, signup_source, etc.) without
    -- needing a migration for every new bit.
    metadata          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- granted=false rows must have revoked_at OR be the initial decline
    -- (e.g. user unchecked marketing at signup). Enforced softly — app
    -- code is the source of truth for state transitions.
    CONSTRAINT user_consents_revoke_consistency CHECK (
        (revoked_at IS NULL) OR (revoked_at >= accepted_at)
    )
);

-- Hot path: "what's the latest consent for this user across all types?"
-- (settings page, signup gate, admin audit).
CREATE INDEX IF NOT EXISTS idx_user_consents_user
    ON public.user_consents (user_id, accepted_at DESC);

-- Hot path: "what's this user's CURRENT consent for type X?"
-- (gate check before AI processing, marketing send, etc.)
CREATE INDEX IF NOT EXISTS idx_user_consents_user_type_latest
    ON public.user_consents (user_id, consent_type, accepted_at DESC);

-- Audit path: "show me everyone who consented to version 1.0 of Terms"
-- (compliance report, version migration impact analysis).
CREATE INDEX IF NOT EXISTS idx_user_consents_type_version
    ON public.user_consents (consent_type, document_version, accepted_at DESC);

-- Group path: "show all consents from the same signup event."
CREATE INDEX IF NOT EXISTS idx_user_consents_event_group
    ON public.user_consents (event_group_id)
    WHERE event_group_id IS NOT NULL;

COMMIT;

