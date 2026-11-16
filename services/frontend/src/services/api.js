import { getAuthHeaders as getAuthHeadersFromAuth, refreshAccessToken } from './auth'

const API_URL = import.meta.env.VITE_API_URL
if (!API_URL) {
  console.error('VITE_API_URL is not set in environment variables')
}

// Helper for authenticated requests with retry logic and queueing/locking
let isRefreshing = false
let failedQueue = []

const processQueue = (error, token = null) => {
  failedQueue.forEach(prom => {
    if (error) {
      prom.reject(error)
    } else {
      prom.resolve(token)
    }
  })
  failedQueue = []
}

async function emitQuotaExceededEvent(response) {
  if (response.status !== 429) return

  try {
    const clone = response.clone()
    const body = await clone.json()
    if (body && body.error === 'quota_exceeded') {
      window.dispatchEvent(new CustomEvent('quota:exceeded', { detail: body }))
    }
  } catch {
    // Non-JSON 429 or already-consumed body — ignore. Rate-limit
    // 429 (abuse) doesn't carry quota_exceeded so the modal stays
    // closed, which is correct.
  }
}

async function authFetch(url, options = {}) {
  let headers
  try {
    headers = await getAuthHeaders()
  } catch (err) {
    throw err
  }

  if (options.body instanceof FormData) {
    delete headers['Content-Type']
  }

  const finalOptions = {
    ...options,
    headers: {
      ...headers,
      ...options.headers,
    },
  }

  let response = await fetch(url, finalOptions)

  let shouldRetry = false
  if (response.status === 401) {
    shouldRetry = true
  } else if (response.status === 500) {
    try {
      const clone = response.clone()
      const body = await clone.json()
      if (body.code === 'TOKEN_MISSING') {
        shouldRetry = true
      }
    } catch (e) {
      // Ignore json parse error on clone
    }
  }

  if (shouldRetry) {
    if (isRefreshing) {
      return new Promise((resolve, reject) => {
        failedQueue.push({ resolve, reject })
      }).then(async token => {
        finalOptions.headers['Authorization'] = `Bearer ${token}`
        const queuedResponse = await fetch(url, finalOptions)
        await emitQuotaExceededEvent(queuedResponse)
        return queuedResponse
      }).catch(err => {
        throw err
      })
    }

    try {
      isRefreshing = true
      const { access_token } = await refreshAccessToken()
      const newToken = access_token
      processQueue(null, newToken)
      finalOptions.headers['Authorization'] = `Bearer ${newToken}`
      response = await fetch(url, finalOptions)
    } catch (refreshErr) {
      processQueue(refreshErr, null)
      throw new Error('Session expired. Please sign in.')
    } finally {
      isRefreshing = false
    }
  }

  // Issue #39 — global 429 + quota_exceeded detection. We sniff the
  // response without consuming the body so callers still see the raw
  // response. Also called from the queued refresh retry path above.
  await emitQuotaExceededEvent(response)

  return response
}

async function getAuthHeaders() {
  try {
    const headers = getAuthHeadersFromAuth()
    if (!headers.Authorization) {
      throw new Error('Authorization header missing from auth service')
    }
    return headers
  } catch (error) {
    try {
      await refreshAccessToken()
      return getAuthHeadersFromAuth()
    } catch (refreshError) {
      throw new Error('No session found. Please sign in.')
    }
  }
}

// ==================== Calendar ====================

export async function syncCalendar() {
  let providerToken = localStorage.getItem('google_access_token')

  const response = await authFetch(`${API_URL}/sync-calendar`, {
    method: 'POST',
    body: JSON.stringify({
      access_token: providerToken || null,
    }),
  })

  if (!response.ok) {
    const error = await response.json()
    if (error.error && error.error.includes('Failed to fetch calendar events')) {
      throw new Error('Google Calendar bağlantısı gerekiyor. Lütfen Ayarlar sayfasından Google Takvimi bağlayın.')
    }
    throw new Error(error.error || 'Failed to sync calendar')
  }

  return response.json()
}

export async function checkCalendarConnection() {
  const resp = await authFetch(`${API_URL}/auth/calendar-status`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to check calendar status')
  }
  return resp.json()
}

export async function getCalendarConnectURL() {
  const resp = await authFetch(`${API_URL}/auth/google/connect`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to get calendar connect URL')
  }
  return resp.json()
}

export async function disconnectGoogleCalendar() {
  const resp = await authFetch(`${API_URL}/auth/google-disconnect`, {
    method: 'POST',
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to disconnect calendar')
  }
  return resp.json()
}

export async function saveGoogleToken(refreshToken, googleEmail = null) {
  const body = { refresh_token: refreshToken }
  if (googleEmail) {
    body.google_email = googleEmail
  }

  const resp = await authFetch(`${API_URL}/auth/google-token`, {
    method: 'POST',
    body: JSON.stringify(body),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to save Google token')
  }
  return resp.json()
}

export async function startCalendarWatch() {
  const response = await authFetch(`${API_URL}/calendar/watch`, {
    method: 'POST',
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to start calendar watch')
  }
  return response.json()
}

// ==================== Meetings ====================

export async function getMeetings(range, status) {
  const params = new URLSearchParams()
  if (range) params.set('range', range)
  if (status) params.set('status', status)
  const query = params.toString() ? `?${params.toString()}` : ''
  const response = await authFetch(`${API_URL}/meetings${query}`)

  if (!response.ok) {
    const error = await response.json()
    throw new Error(error.error || 'Failed to fetch meetings')
  }

  return response.json()
}

export async function getMeetingDetail(meetingId) {
  const response = await authFetch(`${API_URL}/meetings/${meetingId}`)
  if (!response.ok) {
    const error = await response.json()
    throw new Error(error.error || 'Failed to fetch meeting detail')
  }
  return response.json()
}

export async function updateMeeting(meetingId, payload) {
  const response = await authFetch(`${API_URL}/meetings/${meetingId}`, {
    method: 'PATCH',
    body: JSON.stringify(payload),
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({}))
    throw new Error(error.error || 'Toplantı güncellenemedi')
  }
  return response.json()
}

export async function respondMeeting(meetingId, response) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}/respond`, {
    method: 'PATCH',
    body: JSON.stringify({ response }),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to update response')
  }
  return resp.json()
}

export async function cancelMeeting(meetingId) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}`, {
    method: 'DELETE',
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to cancel meeting')
  }
  return resp.json()
}

export async function getFullTranscript(meetingId) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}/full-transcript`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to fetch transcript')
  }
  return resp.json()
}

// ==================== Bot ====================

export async function joinMeetingWithBot(meetingId, meetingUrl) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}/join`, {
    method: 'POST',
    body: JSON.stringify({ meeting_url: meetingUrl }),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to join meeting with bot')
  }
  return resp.json()
}

export async function toggleBotStatus(meetingId, enabled) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}/bot`, {
    method: 'PATCH',
    body: JSON.stringify({ auto_bot_enabled: enabled }),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to toggle bot status')
  }
  return resp.json()
}

// ==================== Profile ====================

export async function getProfile() {
  const response = await authFetch(`${API_URL}/profile`)
  if (!response.ok) {
    const error = await response.json()
    throw new Error(error.error || 'Failed to fetch profile')
  }
  return response.json()
}

export async function updateProfile(profileData) {
  const response = await authFetch(`${API_URL}/profile`, {
    method: 'PUT',
    body: JSON.stringify({
      full_name: profileData.full_name,
      email: profileData.email,
      job_title: profileData.job_title,
      company: profileData.company,
      timezone: profileData.timezone,
      default_meeting_duration: profileData.default_meeting_duration,
      work_start_hour: profileData.work_start_hour,
      work_end_hour: profileData.work_end_hour,
      bot_name: profileData.bot_name,
      bot_language: profileData.bot_language,
      interface_language: profileData.interface_language,
      brief_lead_time_minutes: profileData.brief_lead_time_minutes,
      recording_method: profileData.recording_method,
    }),
  })

  if (!response.ok) {
    const error = await response.json()
    throw new Error(error.error || 'Failed to update profile')
  }

  return response.json()
}

// Phase 5 — per-meeting override. null clears the override so the
// profile default kicks back in. Returns { recording_method_override, effective_method }.
export async function updateMeetingRecordingMethod(meetingId, method) {
  const response = await authFetch(`${API_URL}/meetings/${meetingId}/recording-method`, {
    method: 'PATCH',
    body: JSON.stringify({ method }), // method ∈ {"bot", "desktop", null}
  })
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'request failed' }))
    throw new Error(error.error || 'Failed to update recording method')
  }
  return response.json()
}

// ==================== Decision Items ====================

export async function getDecisionItems(meetingId) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}/decision-items`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to fetch decision items')
  }
  return resp.json()
}

export async function updateDecisionItem(itemId, text, ownerName) {
  const resp = await authFetch(`${API_URL}/decision-items/${itemId}`, {
    method: 'PATCH',
    body: JSON.stringify({ text, owner_name: ownerName }),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to update item')
  }
  return resp.json()
}

export async function completeDecisionItem(itemId) {
  const resp = await authFetch(`${API_URL}/decision-items/${itemId}/complete`, { method: 'POST' })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to complete item')
  }
  return resp.json()
}

export async function dismissDecisionItem(itemId) {
  const resp = await authFetch(`${API_URL}/decision-items/${itemId}/dismiss`, { method: 'POST' })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to dismiss item')
  }
  return resp.json()
}

export async function deleteDecisionItem(itemId) {
  const resp = await authFetch(`${API_URL}/decision-items/${itemId}`, { method: 'DELETE' })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to delete item')
  }
  return resp.json()
}

export async function retryExtraction(meetingId) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}/extract`, { method: 'POST' })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to retry extraction')
  }
  return resp.json()
}

// ==================== Chains ====================

// updateMeetingChain — manual move (chainId set) or remove (chainId=null).
// Backend writes provenance (manual_user / manual_removed) and locks the
// assignment so the auto matcher won't override the user's choice.
export async function updateMeetingChain(meetingId, chainId) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}/chain`, {
    method: 'PATCH',
    body: JSON.stringify({ chain_id: chainId || null }),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to update meeting chain')
  }
  return resp.json()
}

// retryAutoDetectChain — clears the manual lock so the next ingest pass
// can re-evaluate this meeting's chain assignment. Keeps current chain_id
// in place until the matcher decides differently.
export async function retryAutoDetectChain(meetingId) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}/chain`, {
    method: 'PATCH',
    body: JSON.stringify({ action: 'retry_auto_detect' }),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to retry auto-detect')
  }
  return resp.json()
}

// listUserChains — backs the move-to-chain picker. Returns the user's
// chains ordered by most-recent meeting first. Caller filters out the
// current chain client-side (the endpoint is intentionally generic).
export async function listUserChains(limit = 100) {
  const qs = limit ? `?${new URLSearchParams({ limit: String(limit) }).toString()}` : ''
  const resp = await authFetch(`${API_URL}/chains${qs}`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Zincirler yuklenemedi')
  }
  const data = await resp.json()
  return data.chains || []
}

// listChainSuggestions — pending LLM suggestions for the current user.
// Empty array is the normal state until Phase 4 starts producing pending
// rows from the metadata-fallback band.
export async function listChainSuggestions(limit = 20) {
  const qs = limit ? `?${new URLSearchParams({ limit: String(limit) }).toString()}` : ''
  const resp = await authFetch(`${API_URL}/chain-suggestions${qs}`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to load chain suggestions')
  }
  const data = await resp.json()
  return data.suggestions || []
}

export async function acceptChainSuggestion(decisionId) {
  const resp = await authFetch(`${API_URL}/chain-suggestions/${decisionId}/accept`, {
    method: 'POST',
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to accept suggestion')
  }
  return resp.json()
}

export async function declineChainSuggestion(decisionId) {
  const resp = await authFetch(`${API_URL}/chain-suggestions/${decisionId}/decline`, {
    method: 'POST',
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to decline suggestion')
  }
  return resp.json()
}

// ==================== Meeting Notes ====================

export async function getMeetingNote(meetingId) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}/notes`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to fetch note')
  }
  return resp.json()
}

export async function saveMeetingNote(meetingId, content) {
  const resp = await authFetch(`${API_URL}/meetings/${meetingId}/notes`, {
    method: 'POST',
    body: JSON.stringify({ content }),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to save note')
  }
  return resp.json()
}

// ==================== Briefs ====================

export async function getUpcomingBriefs() {
  const resp = await authFetch(`${API_URL}/briefs/upcoming`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to fetch briefs')
  }
  return resp.json()
}

export async function getBriefDetail(meetingId) {
  const resp = await authFetch(`${API_URL}/briefs/${meetingId}`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to fetch brief')
  }
  return resp.json()
}

export async function submitBriefFeedback(meetingId, feedback, note) {
  const resp = await authFetch(`${API_URL}/briefs/${meetingId}/feedback`, {
    method: 'POST',
    body: JSON.stringify({ feedback, note }),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Failed to submit feedback')
  }
  return resp.json()
}

// ==================== Brief delivery preferences (Phase 6) ====================
//
// Narrow-surface helpers for /settings/notifications. updateNotificationPreferences
// is intentionally a PATCH — sending only the toggled field preserves the
// other preference server-side. restoreEmailSubscription is JWT-authed;
// the public GET /api/unsubscribe (token-signed) handles the footer link
// in outgoing mails.

export async function updateNotificationPreferences(prefs) {
  const resp = await authFetch(`${API_URL}/profile/notifications`, {
    method: 'PATCH',
    body: JSON.stringify(prefs),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Tercihler guncellenemedi')
  }
  return resp.json()
}

export async function restoreEmailSubscription() {
  const resp = await authFetch(`${API_URL}/unsubscribe/restore`, { method: 'POST' })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Abonelik yeniden aktif edilemedi')
  }
  return resp.json()
}

// ==================== Slack integration (Phase 4-6) ====================

export async function getSlackStatus() {
  const resp = await authFetch(`${API_URL}/integrations/slack/status`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Slack durumu alinamadi')
  }
  return resp.json()
}

// startSlackInstall fetches the signed Slack consent URL from the
// backend (JWT-authed) and returns it so the caller can navigate the
// browser there via window.location.assign. We can't navigate directly
// to /integrations/slack/install because a full-page nav doesn't carry
// the Authorization header.
export async function startSlackInstall() {
  const resp = await authFetch(`${API_URL}/integrations/slack/install`)
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Slack install akisi baslatilamadi')
  }
  const data = await resp.json()
  if (!data.url) {
    throw new Error('Slack install URL bos dondu')
  }
  return data.url
}

export async function resolveSlackEmail(email) {
  const resp = await authFetch(`${API_URL}/integrations/slack/resolve-email`, {
    method: 'POST',
    body: JSON.stringify({ email }),
  })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    // 404 carries the "not in workspace" error — surface the server's
    // Turkish copy untouched.
    throw new Error(error.error || 'Slack email bulunamadi')
  }
  return resp.json()
}

export async function disconnectSlack() {
  const resp = await authFetch(`${API_URL}/integrations/slack/disconnect`, { method: 'POST' })
  if (!resp.ok) {
    const error = await resp.json().catch(() => ({}))
    throw new Error(error.error || 'Slack baglantisi kesilirken hata')
  }
  return resp.json()
}

// ==================== Quota (issue #39) ====================

// getMyQuota returns the per-user soft-cap status. Backend deliberately
// omits dollar fields — UI consumes yüzde + reset tarihi only.
//   { tier, used_pct, cycle_starts_at, cycle_resets_at }
export async function getMyQuota() {
  const response = await authFetch(`${API_URL}/usage/me`)
  if (!response.ok) {
    const error = await response.json().catch(() => ({}))
    throw new Error(error.error || 'Kota durumu alınamadı')
  }
  return response.json()
}

// ==================== Flows ====================

export async function getFlowData(start, end, scope = 'my') {
  const params = new URLSearchParams()
  if (start) params.set('start', start)
  if (end) params.set('end', end)
  if (scope) params.set('scope', scope)
  const query = params.toString() ? `?${params.toString()}` : ''
  const response = await authFetch(`${API_URL}/flows/data${query}`)
  if (!response.ok) {
    const error = await response.json().catch(() => ({}))
    throw new Error(error.error || 'Akis verileri yuklenemedi')
  }
  return response.json()
}

// ============================================================================
// Phase 7 — Teams API (D-24)
// ============================================================================
//
// NOTE: API_URL already includes the `/api` prefix (see existing getProfile +
// other endpoints in this file). Backend mounts /api/teams/* via routes.go;
// from the frontend we hit it as `${API_URL}/teams/*`.
//
// Each function follows the consistent error-shape pattern: on non-2xx parse
// the body for { error, code, field }, throw an Error with .status, .code,
// and (where relevant) .field attached. authFetch handles JWT refresh + retry.

/**
 * Trigger team detection. Reads userID from the JWT server-side; no body
 * required. Returns the Phase 6 DetectionResult JSON verbatim:
 *   { status, empty_reason, teams: [...], run_id }
 *
 * Throws Error with .status + .code on non-2xx. Notable codes:
 *   - 409 DETECT_RUNNING — silent no-op (UI keeps button disabled)
 *   - 401 GOOGLE_AUTH_EXPIRED — UI prompts re-auth
 *   - 503 FETCH_TRANSIENT — UI offers retry
 *   - 502 FETCH_PERMANENT — UI offers skip
 */
export async function detectTeams() {
  const response = await authFetch(`${API_URL}/teams/detect`, { method: 'POST' })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    const err = new Error(body.error || 'detect failed')
    err.status = response.status
    err.code = body.code
    throw err
  }
  return response.json()
}

/**
 * Save the edited team list (single atomic transaction server-side).
 *
 * payload: { teams: [{ name, members: [{ name, title?, email }] }] }
 * Returns: { status: 'completed', teams: [TeamWithMembers] }
 * Throws Error.code:
 *   - 'ALREADY_COMPLETED' (409) — duplicate-tab race
 *   - 'INVALID_EMAIL' / 'INVALID_NAME' / 'DUPLICATE_TEAM_NAME' /
 *     'DUPLICATE_MEMBER_EMAIL' / 'INVALID_TEAM_ID' / 'INVALID_TITLE' (400)
 */
export async function saveTeams(payload) {
  const response = await authFetch(`${API_URL}/teams/save`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    const err = new Error(body.error || 'save failed')
    err.status = response.status
    err.code = body.code
    err.field = body.field
    throw err
  }
  return response.json()
}

/**
 * Finalize onboarding without saving any teams.
 *
 * emptyState: bool — true ⇒ status=empty_state_skipped, false ⇒ status=skipped
 * Returns: { status: 'skipped' | 'empty_state_skipped' }
 * Throws Error.code='ALREADY_COMPLETED' (409) when status is already finalized.
 */
export async function skipOnboarding(emptyState) {
  const response = await authFetch(`${API_URL}/teams/skip`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ empty_state: !!emptyState }),
  })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    const err = new Error(body.error || 'skip failed')
    err.status = response.status
    err.code = body.code
    throw err
  }
  return response.json()
}

/**
 * Read saved teams + members for the authenticated user.
 *
 * Returns: { teams: [TeamWithMembers] } — empty array (not null) when no teams.
 */
export async function listTeams() {
  const response = await authFetch(`${API_URL}/teams`)
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    const err = new Error(body.error || 'list failed')
    err.status = response.status
    throw err
  }
  return response.json()
}

/**
 * Hard-delete a single team member (KVKK right-to-erasure).
 *
 * memberId: uuid string. Returns nothing on success (204).
 * Throws Error with .status=404 when the member is not found or is owned
 * by another user (IDOR mitigation: both cases collapse to 404).
 */
export async function deleteTeamMember(memberId) {
  const response = await authFetch(`${API_URL}/teams/members/${memberId}`, {
    method: 'DELETE',
  })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    const err = new Error(body.error || 'delete member failed')
    err.status = response.status
    throw err
  }
  // 204 — no body to parse.
}
