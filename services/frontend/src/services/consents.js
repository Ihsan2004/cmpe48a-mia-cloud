import { getAccessToken } from './auth'

const API_URL = import.meta.env.VITE_API_URL

// Module-level cache so multiple ProtectedRoute mounts in a single
// navigation don't all hit /consents/status. Invalidated on successful
// recordConsents() / revokeConsent() and on logout (caller-driven).
//
// `cachedStatus` shape: { satisfied, missing, outdated, revoked } | null
// `inflight` holds the unresolved Promise so concurrent callers share
// one network request.
let cachedStatus = null
let inflight = null

export function invalidateConsentStatusCache() {
    cachedStatus = null
    inflight = null
}

// Resolve once, share across callers. Caller decides what to do with
// a falsy result (network failure) — typically be lenient and let the
// app render; the gate page itself will re-fetch with a visible error.
export async function getConsentStatus({ force = false } = {}) {
    if (!force && cachedStatus) return cachedStatus
    if (inflight) return inflight

    const token = getAccessToken()
    if (!token) return null

    inflight = (async () => {
        try {
            const response = await fetch(`${API_URL}/consents/status`, {
                method: 'GET',
                headers: { Authorization: `Bearer ${token}` },
            })
            if (!response.ok) {
                throw new Error(`status HTTP ${response.status}`)
            }
            const data = await response.json()
            cachedStatus = data
            return data
        } finally {
            inflight = null
        }
    })()
    return inflight
}

export async function recordConsents(consents) {
    const token = getAccessToken()
    if (!token) throw new Error('Not authenticated')

    const response = await fetch(`${API_URL}/consents`, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({ consents }),
    })

    if (!response.ok) {
        let serverMessage = ''
        let code = ''
        try {
            const data = await response.json()
            serverMessage = data.error || data.detail || ''
            code = data.code || ''
        } catch (e) {
            // ignore parse error
        }
        const err = new Error(serverMessage || `Onay kaydı başarısız (HTTP ${response.status})`)
        err.status = response.status
        err.code = code
        throw err
    }

    invalidateConsentStatusCache()
    return response.json()
}

export async function listConsents() {
    const token = getAccessToken()
    if (!token) throw new Error('Not authenticated')

    const response = await fetch(`${API_URL}/consents`, {
        method: 'GET',
        headers: { Authorization: `Bearer ${token}` },
    })

    if (!response.ok) {
        throw new Error(`Onaylar listelenemedi (HTTP ${response.status})`)
    }
    return response.json()
}

export async function revokeConsent(type, reason = '') {
    const token = getAccessToken()
    if (!token) throw new Error('Not authenticated')

    const response = await fetch(`${API_URL}/consents/revoke`, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({ type, reason }),
    })

    if (!response.ok) {
        let serverMessage = ''
        let code = ''
        try {
            const data = await response.json()
            serverMessage = data.error || ''
            code = data.code || ''
        } catch (e) {
            // ignore parse error
        }
        const err = new Error(serverMessage || `Onay geri çekilemedi (HTTP ${response.status})`)
        err.status = response.status
        err.code = code
        throw err
    }

    invalidateConsentStatusCache()
    return response.json()
}
