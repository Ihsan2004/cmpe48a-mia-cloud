// Consent type + version constants. MUST stay in sync with:
//   - backend/internal/services/consent_service.go (CurrentXxxVersion)
//   - frontend/src/pages/{TermsOfService,PrivacyPolicy,KvkkAydinlatma,
//     PilotSozlesmesi}.jsx ("Sürüm: X.Y" line)
//
// Bump the version here AND in the corresponding metin file when the
// legal text changes — backend rejects stale versions with HTTP 409.

export const CURRENT_VERSIONS = {
    terms_of_service: '1.0',
    privacy_policy: '1.0',
    kvkk_aydinlatma: '1.0',
    cross_border_transfer: '1.0',
    ai_processing: '1.0',
    pilot_sozlesmesi: '1.0',
    marketing: '1.0',
}

// What signup must include with granted=true. Mirror of
// models.MandatoryConsentTypes in Go.
export const MANDATORY_CONSENT_TYPES = [
    'terms_of_service',
    'privacy_policy',
    'kvkk_aydinlatma',
    'cross_border_transfer',
    'ai_processing',
]

// Build a consents payload from a status object returned by
// /consents/status. Covers missing + outdated + revoked entries
// (all mandatory types) at their CURRENT_VERSIONS with granted=true.
// Used by the consent gate page when the user re-accepts.
export function buildReacceptConsents(status) {
    if (!status) return []
    const types = new Set()
    for (const t of status.missing || []) types.add(t)
    for (const o of status.outdated || []) types.add(o.type)
    for (const t of status.revoked || []) types.add(t)
    return Array.from(types).map((type) => ({
        type,
        version: CURRENT_VERSIONS[type],
        granted: true,
    }))
}

// Build the consents array the signup endpoint expects. Mandatory
// bundle is recorded as granted=acceptedMandatory (which must be
// true for the form to submit). Marketing is recorded either way so
// we have a positive opt-out record.
export function buildSignupConsents({ acceptedMandatory, acceptedMarketing }) {
    const items = MANDATORY_CONSENT_TYPES.map((type) => ({
        type,
        version: CURRENT_VERSIONS[type],
        granted: !!acceptedMandatory,
    }))
    items.push({
        type: 'marketing',
        version: CURRENT_VERSIONS.marketing,
        granted: !!acceptedMarketing,
    })
    return items
}

// sessionStorage key for consents queued before a Google OAuth
// redirect. After the callback, OAuthCallback reads them, POSTs to
// /api/consents, then clears the key on success.
const PENDING_CONSENTS_KEY = 'syncrun_pending_consents'

export function savePendingConsents(consents) {
    try {
        sessionStorage.setItem(PENDING_CONSENTS_KEY, JSON.stringify(consents))
    } catch (e) {
        // Quota or privacy-mode failure — log but don't break signup.
        // Worst case: the user lands without consents and the gate
        // page (Faz 1.5c) re-prompts them.
        console.warn('savePendingConsents failed', e)
    }
}

export function loadPendingConsents() {
    try {
        const raw = sessionStorage.getItem(PENDING_CONSENTS_KEY)
        if (!raw) return null
        const parsed = JSON.parse(raw)
        if (!Array.isArray(parsed)) return null
        return parsed
    } catch (e) {
        return null
    }
}

export function clearPendingConsents() {
    try {
        sessionStorage.removeItem(PENDING_CONSENTS_KEY)
    } catch (e) {
        // ignore
    }
}

// POST the queued consents to /api/consents with the freshly-issued
// access token. Returns a promise that resolves on success and
// rejects with an Error on failure. The caller (OAuthCallback)
// decides whether to retry, surface to UI, or redirect to the gate.
//
// On 2xx response, sessionStorage is cleared. On non-2xx, the queue
// is kept so a retry / gate page can still see it.
export async function recordPendingConsents(apiUrl, accessToken) {
    const consents = loadPendingConsents()
    if (!consents || consents.length === 0) {
        return { recorded: false, reason: 'no_pending' }
    }

    const response = await fetch(`${apiUrl}/consents`, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${accessToken}`,
        },
        body: JSON.stringify({ consents }),
    })

    if (!response.ok) {
        let serverMessage = ''
        try {
            const data = await response.json()
            serverMessage = data.error || data.detail || ''
        } catch (e) {
            // ignore parse error
        }
        const err = new Error(serverMessage || `Consent kayıt başarısız (HTTP ${response.status})`)
        err.status = response.status
        throw err
    }

    clearPendingConsents()
    return { recorded: true }
}
