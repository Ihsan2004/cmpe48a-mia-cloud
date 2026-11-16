-- ============================================================
-- MVP Migration: Decision Tracker + Pre-Meeting Brief
-- Date: 2026-03-29
-- Run this in Supabase SQL Editor after 001_mvp_schema.sql
-- ============================================================

-- 1. Meeting Chains
CREATE TABLE IF NOT EXISTS public.meeting_chains (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chain_type TEXT NOT NULL CHECK (chain_type IN ('calendar_series', 'participant_overlap_title')),
    calendar_series_id TEXT,
    participant_hash TEXT,
    display_name TEXT,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_meeting_chains_series ON public.meeting_chains (calendar_series_id) WHERE calendar_series_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_meeting_chains_type ON public.meeting_chains (chain_type);

-- 2. Decision Items (decision, action_item, open_topic — tek tablo)
CREATE TABLE IF NOT EXISTS public.decision_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id UUID NOT NULL REFERENCES public.meetings(id) ON DELETE CASCADE,
    chain_id UUID REFERENCES public.meeting_chains(id),
    user_id UUID NOT NULL REFERENCES public.users(id),
    item_type TEXT NOT NULL CHECK (item_type IN ('decision', 'action_item', 'open_topic')),
    text TEXT NOT NULL,
    owner_name TEXT,
    source_quote TEXT,
    status TEXT NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed', 'confirmed', 'completed', 'dismissed')),
    confirmed_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_decision_items_meeting ON public.decision_items (meeting_id);
CREATE INDEX IF NOT EXISTS idx_decision_items_chain_status ON public.decision_items (chain_id, status);
CREATE INDEX IF NOT EXISTS idx_decision_items_user ON public.decision_items (user_id);
CREATE INDEX IF NOT EXISTS idx_decision_items_type ON public.decision_items (item_type, status);

-- 3. Meeting Notes (kişisel, kullanıcı başına toplantı başına tek not)
CREATE TABLE IF NOT EXISTS public.meeting_notes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id UUID NOT NULL REFERENCES public.meetings(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES public.users(id),
    content TEXT,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE(meeting_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_meeting_notes_meeting_user ON public.meeting_notes (meeting_id, user_id);

-- 4. Meeting Briefs (cache — kullanıcı başına toplantı başına tek brief)
CREATE TABLE IF NOT EXISTS public.meeting_briefs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.users(id),
    target_meeting_id UUID NOT NULL REFERENCES public.meetings(id),
    chain_id UUID REFERENCES public.meeting_chains(id),
    content TEXT NOT NULL,
    generation_method TEXT NOT NULL DEFAULT 'llm' CHECK (generation_method IN ('llm', 'template_fallback')),
    opened_at TIMESTAMPTZ,
    email_sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE(user_id, target_meeting_id)
);

CREATE INDEX IF NOT EXISTS idx_meeting_briefs_user_meeting ON public.meeting_briefs (user_id, target_meeting_id);
CREATE INDEX IF NOT EXISTS idx_meeting_briefs_target ON public.meeting_briefs (target_meeting_id);

-- 5. Brief Feedback (kullanıcı başına meeting başına tek feedback — upsert)
CREATE TABLE IF NOT EXISTS public.brief_feedback (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.users(id),
    target_meeting_id UUID NOT NULL REFERENCES public.meetings(id),
    brief_id UUID REFERENCES public.meeting_briefs(id),
    feedback TEXT NOT NULL CHECK (feedback IN ('useful', 'not_useful')),
    note TEXT,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE(user_id, target_meeting_id)
);

-- 6. Meetings tablosuna yeni alanlar
ALTER TABLE public.meetings ADD COLUMN IF NOT EXISTS calendar_series_id TEXT;
ALTER TABLE public.meetings ADD COLUMN IF NOT EXISTS chain_id UUID REFERENCES public.meeting_chains(id);
ALTER TABLE public.meetings ADD COLUMN IF NOT EXISTS processed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_meetings_chain ON public.meetings (chain_id) WHERE chain_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_meetings_calendar_series ON public.meetings (calendar_series_id) WHERE calendar_series_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_meetings_processed ON public.meetings (processed_at) WHERE processed_at IS NOT NULL;

-- 7. Profiles tablosuna brief timing
ALTER TABLE public.profiles ADD COLUMN IF NOT EXISTS brief_lead_time_minutes INTEGER DEFAULT 10;
