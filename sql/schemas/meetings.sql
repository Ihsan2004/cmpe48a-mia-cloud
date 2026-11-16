CREATE TABLE public.meetings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ NOT NULL,
    meeting_url TEXT,
    google_event_id TEXT NOT NULL,
    description TEXT,
    location TEXT,
    attendees JSONB DEFAULT '[]'::jsonb,
    organizer_email TEXT,
    auto_bot_enabled BOOLEAN DEFAULT true,
    bot_status TEXT,
    bot_joined_at TIMESTAMPTZ,
    last_heartbeat_at TIMESTAMPTZ,
    agenda_items TEXT,
    expected_outcomes TEXT,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_meetings_user_id ON public.meetings (user_id);
CREATE INDEX idx_meetings_start_time ON public.meetings (start_time);
CREATE INDEX idx_meetings_google_event_id ON public.meetings (google_event_id);
CREATE INDEX idx_meetings_meeting_url ON public.meetings (meeting_url) WHERE meeting_url IS NOT NULL;
CREATE INDEX idx_meetings_auto_bot_enabled ON public.meetings (auto_bot_enabled) WHERE auto_bot_enabled = true;
CREATE INDEX idx_meetings_bot_status ON public.meetings (bot_status) WHERE bot_status IS NOT NULL;
CREATE INDEX idx_meetings_bot_watchdog ON public.meetings (bot_status, last_heartbeat_at);
