CREATE TABLE public.transcript_segments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id UUID REFERENCES public.meetings(id) ON DELETE SET NULL,
    meeting_url TEXT,
    text TEXT NOT NULL,
    start_time DOUBLE PRECISION NOT NULL,
    end_time DOUBLE PRECISION NOT NULL,
    duration DOUBLE PRECISION GENERATED ALWAYS AS (end_time - start_time) STORED,
    speaker TEXT,
    role TEXT NOT NULL,
    stt_provider TEXT,
    metadata JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ DEFAULT now(),
    idempotency_key TEXT UNIQUE
);

CREATE INDEX idx_transcript_segments_meeting_id ON public.transcript_segments (meeting_id);
CREATE INDEX idx_transcript_segments_start_time ON public.transcript_segments (start_time);
CREATE INDEX idx_transcript_segments_created_at ON public.transcript_segments (created_at DESC);
CREATE INDEX idx_transcript_segments_speaker ON public.transcript_segments (speaker) WHERE speaker IS NOT NULL;
CREATE INDEX idx_transcript_segments_idempotency ON public.transcript_segments (idempotency_key) WHERE idempotency_key IS NOT NULL;
