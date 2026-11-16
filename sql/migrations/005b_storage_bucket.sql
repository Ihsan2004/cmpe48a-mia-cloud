-- Migration 005b: meeting-audio bucket + storage RLS
-- Complements 005_batch_transcription.sql. Creates the Supabase Storage
-- bucket the upload pipeline writes to and locks it down to service_role.
--
-- Access model (Phase 1 roadmap, locked decision):
--   Backend-proxy only. Frontend NEVER touches Supabase Storage directly.
--   All reads/writes/deletes go through api.syncrun.net, authenticated
--   server-side with SUPABASE_SERVICE_ROLE_KEY. That bypass is granted
--   automatically by Supabase for the service_role — no policy needed
--   for the backend path.
--
--   The two explicit policies below exist to:
--     - Document intent ("only service_role touches this bucket")
--     - Defend against future accidental RLS-off moves
--     - Give dashboard operators a deny-by-default view
--
-- All statements are idempotent.

-- ============================================================
-- 1) Create the bucket (private)
-- ============================================================

INSERT INTO storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
VALUES (
    'meeting-audio',
    'meeting-audio',
    false,                       -- NOT public
    2147483648,                  -- 2 GiB per object (3h meeting @ 64kbps ≈ 90 MB; headroom for manifest+edge cases)
    ARRAY['audio/webm', 'audio/ogg', 'video/webm']
)
ON CONFLICT (id) DO UPDATE
    SET file_size_limit = EXCLUDED.file_size_limit,
        allowed_mime_types = EXCLUDED.allowed_mime_types;

-- ============================================================
-- 2) RLS policies — service_role only
-- ============================================================

-- RLS on storage.objects is enabled by Supabase default. We add explicit
-- ALL-ops policy for service_role on this bucket. Anon / authenticated
-- roles have NO policy → deny by default.

DROP POLICY IF EXISTS "meeting_audio_service_role_all" ON storage.objects;

CREATE POLICY "meeting_audio_service_role_all"
    ON storage.objects
    FOR ALL
    TO service_role
    USING (bucket_id = 'meeting-audio')
    WITH CHECK (bucket_id = 'meeting-audio');

-- ============================================================
-- 3) Verification (run manually, not part of migration)
-- ============================================================
-- SELECT id, name, public, file_size_limit FROM storage.buckets WHERE id = 'meeting-audio';
-- SELECT policyname, cmd, roles FROM pg_policies
--   WHERE schemaname = 'storage' AND tablename = 'objects'
--     AND policyname = 'meeting_audio_service_role_all';
