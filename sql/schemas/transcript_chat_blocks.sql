CREATE TABLE public.transcript_chat_blocks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id UUID NOT NULL REFERENCES public.meetings(id) ON DELETE CASCADE,
    speaker TEXT NOT NULL,
    text TEXT NOT NULL,
    start_time DOUBLE PRECISION NOT NULL,
    end_time DOUBLE PRECISION NOT NULL,
    raw_segment_ids JSONB,
    is_noise BOOLEAN DEFAULT false,
    embedding vector(1536),
    embedding_model VARCHAR(50) DEFAULT 'text-embedding-3-small',
    embedding_generated_at TIMESTAMP,
    is_finalized BOOLEAN DEFAULT false,
    finalized_at TIMESTAMPTZ,
    source_segment_count INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_chat_blocks_not_noise ON public.transcript_chat_blocks (meeting_id, is_noise) WHERE is_noise = false;
CREATE INDEX idx_chat_blocks_finalized ON public.transcript_chat_blocks (meeting_id, is_finalized, start_time);
CREATE INDEX idx_chat_blocks_meeting_start ON public.transcript_chat_blocks (meeting_id, start_time);
CREATE INDEX idx_chat_blocks_embedding ON public.transcript_chat_blocks USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
CREATE INDEX idx_chat_blocks_embedding_status ON public.transcript_chat_blocks (embedding_generated_at) WHERE embedding IS NOT NULL;
