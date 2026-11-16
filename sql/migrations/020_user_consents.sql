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
