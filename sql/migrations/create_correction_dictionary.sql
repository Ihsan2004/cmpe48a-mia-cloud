-- Migration: Create correction_dictionary table
-- Date: 2026-03-22
-- Description:
--   Domain-specific STT error correction terms.
--   Used by CorrectionService to inject known wrong->correct mappings
--   into the GPT correction prompt, improving transcript quality for
--   product-specific terminology that STT engines frequently misrecognize.

CREATE TABLE IF NOT EXISTS public.correction_dictionary (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wrong_form    TEXT NOT NULL,
    correct_form  TEXT NOT NULL,
    description   TEXT,
    is_active     BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.correction_dictionary IS 'Domain-specific STT error correction terms for transcript quality improvement';
COMMENT ON COLUMN public.correction_dictionary.wrong_form IS 'The incorrect form as typically produced by STT (e.g. "pre miting", "miya")';
COMMENT ON COLUMN public.correction_dictionary.correct_form IS 'The correct form to use in corrected transcript (e.g. "pre-meeting", "MIA")';
COMMENT ON COLUMN public.correction_dictionary.description IS 'Optional context for GPT (e.g. "Ürün adı", "Teknik terim")';
COMMENT ON COLUMN public.correction_dictionary.is_active IS 'Soft delete / toggle for terms without removing them';

CREATE INDEX IF NOT EXISTS idx_correction_dictionary_active
    ON public.correction_dictionary (is_active) WHERE is_active = true;

-- Seed common domain terms
INSERT INTO public.correction_dictionary (wrong_form, correct_form, description) VALUES
    ('miya', 'MIA', 'Ürün adı'),
    ('mia', 'MIA', 'Ürün adı'),
    ('pre miting', 'pre-meeting', 'Toplantı öncesi hazırlık'),
    ('pre meeting', 'pre-meeting', 'Toplantı öncesi hazırlık'),
    ('premiting', 'pre-meeting', 'Toplantı öncesi hazırlık'),
    ('post miting', 'post-meeting', 'Toplantı sonrası'),
    ('pos meeting', 'post-meeting', 'Toplantı sonrası'),
    ('aksiyon aytem', 'action item', 'Görev/yapılacak iş'),
    ('aksiyon aytım', 'action item', 'Görev/yapılacak iş'),
    ('elevenlebs', 'ElevenLabs', 'Ses teknolojisi sağlayıcısı'),
    ('eleven labs', 'ElevenLabs', 'Ses teknolojisi sağlayıcısı'),
    ('open ei ay', 'OpenAI', 'AI sağlayıcısı'),
    ('open ay ay', 'OpenAI', 'AI sağlayıcısı'),
    ('çet cipi ti', 'ChatGPT', 'OpenAI chatbot'),
    ('cipi ti', 'GPT', 'AI model ailesi'),
    ('rag', 'RAG', 'Retrieval Augmented Generation'),
    ('supabase', 'Supabase', 'Veritabanı platformu'),
    ('süperbeys', 'Supabase', 'Veritabanı platformu'),
    ('speach tu tekst', 'speech-to-text', 'Konuşmadan metne çevirme'),
    ('tekst tu speach', 'text-to-speech', 'Metinden konuşmaya çevirme'),
    ('STT', 'STT', 'Speech-to-Text kısaltması'),
    ('TTS', 'TTS', 'Text-to-Speech kısaltması')
ON CONFLICT DO NOTHING;
