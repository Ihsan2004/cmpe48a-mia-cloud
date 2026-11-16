import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { Bell, Mail, MessageSquare, CheckCircle2, AlertTriangle, Unlink, Link as LinkIcon, Loader, Save } from 'lucide-react'
import {
  getProfile,
  updateNotificationPreferences,
  restoreEmailSubscription,
  getSlackStatus,
  startSlackInstall,
  resolveSlackEmail,
  disconnectSlack,
} from '../services/api'
import LoadingState from '../components/LoadingState'
import { showError, showSuccess } from '../utils/toast'

// SettingsNotifications is the /settings/notifications page (brief
// delivery roadmap Phase 6). It owns three independent state machines:
//
//   1. Email toggle — opt-in boolean. Separate "Yeniden aç" flow when
//      the user has been unsubscribed (profiles.email_unsubscribed_at
//      is set).
//   2. Slack toggle — opt-in boolean, only meaningful once the
//      integration is connected.
//   3. Slack integration card — three states:
//        a. not connected      → "Bağla" button → window.location = /install
//        b. connected, pending → alt-email form → POST /resolve-email
//        c. connected, active  → show team + "Bağlantıyı kes"
//
// The page reads everything with a single pair of GETs on mount
// (/profile + /integrations/slack/status) and updates locally after
// each mutation — no global store.

export default function SettingsNotifications() {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()

  const [loading, setLoading] = useState(true)
  const [profile, setProfile] = useState(null)
  const [slack, setSlack] = useState(null)

  // Per-action loading flags — lets us disable the *specific* button
  // in flight rather than the whole page.
  const [savingEmail, setSavingEmail] = useState(false)
  const [savingSlackToggle, setSavingSlackToggle] = useState(false)
  const [restoringUnsub, setRestoringUnsub] = useState(false)
  const [disconnectingSlack, setDisconnectingSlack] = useState(false)
  const [resolvingEmail, setResolvingEmail] = useState(false)

  const [altEmailInput, setAltEmailInput] = useState('')

  useEffect(() => {
    loadAll()

    // Slack callback handshake — the backend redirects back with one of
    // ?slack=connected|needs_email|error. Toast the result + clear the
    // query so a page refresh doesn't retoast.
    const slackParam = searchParams.get('slack')
    if (slackParam) {
      if (slackParam === 'connected') {
        showSuccess(t('settingsNotifications.slack.toastConnected'))
      } else if (slackParam === 'needs_email') {
        showError(t('settingsNotifications.slack.toastNeedsEmail'))
      } else if (slackParam === 'error') {
        showError(t('settingsNotifications.slack.toastError'))
      }
      const next = new URLSearchParams(searchParams)
      next.delete('slack')
      next.delete('error')
      setSearchParams(next, { replace: true })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function loadAll() {
    try {
      setLoading(true)
      const [p, s] = await Promise.all([getProfile(), getSlackStatus().catch(() => ({ connected: false }))])
      setProfile(p)
      setSlack(s)
    } catch (e) {
      showError(e.message || t('common.error'))
    } finally {
      setLoading(false)
    }
  }

  // ---------- Email toggle ----------

  const isUnsubscribed = !!profile?.email_unsubscribed_at

  async function handleEmailToggle(next) {
    if (savingEmail) return
    setSavingEmail(true)
    try {
      await updateNotificationPreferences({ brief_deliver_via_email: next })
      // PATCH returns {ok:true}; we mirror the new value locally + clear
      // email_unsubscribed_at when opting in (backend does this too).
      setProfile((prev) => ({
        ...prev,
        brief_deliver_via_email: next,
        email_unsubscribed_at: next ? null : prev?.email_unsubscribed_at,
      }))
      showSuccess(t('common.success'))
    } catch (e) {
      showError(e.message)
    } finally {
      setSavingEmail(false)
    }
  }

  async function handleRestoreUnsubscribe() {
    if (restoringUnsub) return
    setRestoringUnsub(true)
    try {
      await restoreEmailSubscription()
      // Restore just clears email_unsubscribed_at. The toggle stays
      // off per roadmap Karar #12 (explicit opt-in); user must still
      // tick the box to actually receive mail.
      setProfile((prev) => ({ ...prev, email_unsubscribed_at: null }))
      showSuccess(t('settingsNotifications.email.restoreSuccess'))
    } catch (e) {
      showError(e.message)
    } finally {
      setRestoringUnsub(false)
    }
  }

  // ---------- Slack toggle ----------

  async function handleSlackToggle(next) {
    if (savingSlackToggle) return
    setSavingSlackToggle(true)
    try {
      await updateNotificationPreferences({ brief_deliver_via_slack: next })
      setProfile((prev) => ({ ...prev, brief_deliver_via_slack: next }))
      showSuccess(t('common.success'))
    } catch (e) {
      showError(e.message)
    } finally {
      setSavingSlackToggle(false)
    }
  }

  // ---------- Slack integration card ----------

  async function handleSlackConnect() {
    // Fetch the signed consent URL first (JWT-authed XHR) — we can't
    // full-page-navigate to the backend /install because the browser
    // would drop the Authorization header. Once we have the URL we
    // hand off to Slack with a normal redirect.
    try {
      const url = await startSlackInstall()
      window.location.assign(url)
    } catch (e) {
      showError(e.message)
    }
  }

  async function handleSlackDisconnect() {
    if (disconnectingSlack) return
    if (!window.confirm(t('settingsNotifications.slack.disconnectConfirm'))) return
    setDisconnectingSlack(true)
    try {
      await disconnectSlack()
      setSlack({ connected: false })
      setProfile((prev) => ({ ...prev, brief_deliver_via_slack: false }))
      showSuccess(t('settingsNotifications.slack.disconnectSuccess'))
    } catch (e) {
      showError(e.message)
    } finally {
      setDisconnectingSlack(false)
    }
  }

  async function handleResolveEmail(e) {
    e.preventDefault()
    if (resolvingEmail) return
    const trimmed = altEmailInput.trim()
    if (!trimmed) return
    setResolvingEmail(true)
    try {
      const resp = await resolveSlackEmail(trimmed)
      setSlack({
        ...slack,
        pending_email: false,
        matched_email: resp.matched_email || trimmed,
      })
      setAltEmailInput('')
      showSuccess(t('settingsNotifications.slack.resolveSuccess'))
    } catch (err) {
      showError(err.message)
    } finally {
      setResolvingEmail(false)
    }
  }

  if (loading) return <LoadingState />

  const emailEnabled = !!profile?.brief_deliver_via_email && !isUnsubscribed
  const slackConnected = !!slack?.connected
  const slackPending = !!slack?.pending_email
  const slackToggleEnabled = !!profile?.brief_deliver_via_slack && slackConnected && !slackPending

  return (
    <div className="max-w-3xl mx-auto px-6 py-10">
      <header className="mb-10">
        <div className="flex items-center gap-3 mb-2">
          <Bell className="w-6 h-6 text-[var(--accent-ochre-ink)]" />
          <h1 className="text-2xl font-semibold text-text-primary">{t('settingsNotifications.title')}</h1>
        </div>
        <p className="text-text-secondary text-sm">{t('settingsNotifications.subtitle')}</p>
      </header>

      {/* ---------- Email ---------- */}
      <section className="bg-[var(--canvas-muted)] border border-[var(--line-hair)] rounded-xl p-5 mb-6">
        <div className="flex items-start gap-3">
          <Mail className="w-5 h-5 mt-0.5 text-text-primary" />
          <div className="flex-1">
            <div className="flex items-center justify-between gap-3">
              <div>
                <h2 className="font-medium text-text-primary">{t('settingsNotifications.email.title')}</h2>
                <p className="text-meta text-text-secondary mt-0.5">{t('settingsNotifications.email.subtitle')}</p>
              </div>
              <Toggle
                checked={emailEnabled}
                onChange={handleEmailToggle}
                disabled={savingEmail || isUnsubscribed}
                ariaLabel={t('settingsNotifications.email.toggle')}
                testId="email-toggle"
              />
            </div>

            {isUnsubscribed && (
              <div className="mt-4 flex items-start gap-2 p-3 rounded-lg bg-amber-50 border border-amber-200 text-amber-900">
                <AlertTriangle className="w-4 h-4 mt-0.5 flex-shrink-0" />
                <div className="flex-1 text-meta">
                  <p>{t('settingsNotifications.email.unsubscribed')}</p>
                  <button
                    type="button"
                    className="mt-2 inline-flex items-center gap-1 text-amber-900 underline hover:opacity-80 disabled:opacity-50"
                    onClick={handleRestoreUnsubscribe}
                    disabled={restoringUnsub}
                    data-testid="restore-unsubscribe"
                  >
                    {restoringUnsub && <Loader className="w-3 h-3 animate-spin" />}
                    {t('settingsNotifications.email.restore')}
                  </button>
                </div>
              </div>
            )}
          </div>
        </div>
      </section>

      {/* ---------- Slack card + toggle ---------- */}
      <section className="bg-[var(--canvas-muted)] border border-[var(--line-hair)] rounded-xl p-5 mb-6">
        <div className="flex items-start gap-3">
          <MessageSquare className="w-5 h-5 mt-0.5 text-text-primary" />
          <div className="flex-1">
            <div className="flex items-center justify-between gap-3 mb-4">
              <div>
                <h2 className="font-medium text-text-primary">{t('settingsNotifications.slack.title')}</h2>
                <p className="text-meta text-text-secondary mt-0.5">{t('settingsNotifications.slack.subtitle')}</p>
              </div>
              <Toggle
                checked={slackToggleEnabled}
                onChange={handleSlackToggle}
                disabled={savingSlackToggle || !slackConnected || slackPending}
                ariaLabel={t('settingsNotifications.slack.toggle')}
                testId="slack-toggle"
              />
            </div>

            <div data-testid="slack-card">
              {!slackConnected && (
                <SlackNotConnected
                  onConnect={handleSlackConnect}
                  label={t('settingsNotifications.slack.connectButton')}
                />
              )}
              {slackConnected && slackPending && (
                <SlackPendingEmail
                  value={altEmailInput}
                  onChange={setAltEmailInput}
                  onSubmit={handleResolveEmail}
                  loading={resolvingEmail}
                  t={t}
                />
              )}
              {slackConnected && !slackPending && (
                <SlackActive
                  teamName={slack.team_name}
                  matchedEmail={slack.matched_email}
                  onDisconnect={handleSlackDisconnect}
                  disconnecting={disconnectingSlack}
                  t={t}
                />
              )}
            </div>
          </div>
        </div>
      </section>

      <p className="text-meta text-text-secondary text-center">
        {t('settingsNotifications.leadTimeHint', { minutes: profile?.brief_lead_time_minutes ?? 10 })}
      </p>
    </div>
  )
}

// ==================== Subcomponents ====================

function Toggle({ checked, onChange, disabled, ariaLabel, testId }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={ariaLabel}
      data-testid={testId}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-[var(--accent-ochre-ink)] focus:ring-offset-1 ${
        checked ? 'bg-[var(--accent-ochre-ink)]' : 'bg-gray-300'
      } ${disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'}`}
    >
      <span
        className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
          checked ? 'translate-x-6' : 'translate-x-1'
        }`}
      />
    </button>
  )
}

function SlackNotConnected({ onConnect, label }) {
  return (
    <button
      type="button"
      onClick={onConnect}
      className="inline-flex items-center gap-2 px-4 py-2 rounded-lg bg-[var(--accent-ochre-ink)] text-white text-sm font-medium hover:opacity-90"
      data-testid="slack-connect"
    >
      <LinkIcon className="w-4 h-4" />
      {label}
    </button>
  )
}

function SlackPendingEmail({ value, onChange, onSubmit, loading, t }) {
  return (
    <form onSubmit={onSubmit} className="space-y-3" data-testid="slack-pending-email">
      <div className="flex items-start gap-2 p-3 rounded-lg bg-amber-50 border border-amber-200 text-amber-900 text-meta">
        <AlertTriangle className="w-4 h-4 mt-0.5 flex-shrink-0" />
        <span>{t('settingsNotifications.slack.pendingEmailHint')}</span>
      </div>
      <div className="flex gap-2">
        <input
          type="email"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={t('settingsNotifications.slack.altEmailPlaceholder')}
          className="flex-1 px-3 py-2 rounded-lg border border-[var(--line-hair)] bg-white text-sm text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-[var(--accent-ochre-ink)]"
          required
          data-testid="slack-alt-email-input"
        />
        <button
          type="submit"
          disabled={loading || !value.trim()}
          className="inline-flex items-center gap-1 px-4 py-2 rounded-lg bg-[var(--accent-ochre-ink)] text-white text-sm font-medium hover:opacity-90 disabled:opacity-50"
          data-testid="slack-alt-email-submit"
        >
          {loading ? <Loader className="w-4 h-4 animate-spin" /> : <Save className="w-4 h-4" />}
          {t('common.confirm')}
        </button>
      </div>
    </form>
  )
}

function SlackActive({ teamName, matchedEmail, onDisconnect, disconnecting, t }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div className="flex items-center gap-2 text-meta">
        <CheckCircle2 className="w-4 h-4 text-green-600" />
        <div>
          <div className="text-text-primary font-medium">{teamName || t('settingsNotifications.slack.connectedFallback')}</div>
          {matchedEmail && <div className="text-text-secondary">{matchedEmail}</div>}
        </div>
      </div>
      <button
        type="button"
        onClick={onDisconnect}
        disabled={disconnecting}
        className="inline-flex items-center gap-1 px-3 py-1.5 rounded-lg border border-[var(--line-hair)] text-text-secondary text-meta hover:bg-white disabled:opacity-50"
        data-testid="slack-disconnect"
      >
        {disconnecting ? <Loader className="w-3 h-3 animate-spin" /> : <Unlink className="w-3 h-3" />}
        {t('settingsNotifications.slack.disconnect')}
      </button>
    </div>
  )
}
