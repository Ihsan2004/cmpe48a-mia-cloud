import { useState, useEffect, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  Calendar, RefreshCw, ExternalLink, Clock, Users, FileText,
  Check, HelpCircle, XCircle, Trash2,
  Play
} from 'lucide-react'
import { syncCalendar, getMeetings, respondMeeting, cancelMeeting, startCalendarWatch, getUpcomingBriefs } from '../services/api'
import { showError, showSuccess } from '../utils/toast'
import LoadingState from '../components/LoadingState'
import EmptyState from '../components/EmptyState'
import ErrorState from '../components/ErrorState'
import BriefCard from '../components/BriefCard'
import MockNextMeetingBriefCard from '../components/MockNextMeetingBriefCard'

function StatusIndicator({ isActive = true }) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center gap-2">
      <span className={`w-1.5 h-1.5 rounded-full ${isActive ? 'bg-[var(--accent-ochre)] animate-pulse' : 'bg-text-tertiary'}`} />
      <span className="text-micro uppercase tracking-[0.14em] text-[var(--accent-ochre-ink)] font-sans">
        {isActive ? t('meeting.active') : t('common.inactive')}
      </span>
    </div>
  )
}

export default function Dashboard() {
  const navigate = useNavigate()
  const { t } = useTranslation()
  const [meetings, setMeetings] = useState([])
  const [briefs, setBriefs] = useState([])
  const [loading, setLoading] = useState(false)
  const [fetchError, setFetchError] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const [showMockBrief, setShowMockBrief] = useState(
    () => localStorage.getItem('mock_brief_card_enabled') === 'true'
  )

  useEffect(() => {
    const onStorage = () =>
      setShowMockBrief(localStorage.getItem('mock_brief_card_enabled') === 'true')
    window.addEventListener('storage', onStorage)
    window.addEventListener('mock_brief_card_changed', onStorage)
    return () => {
      window.removeEventListener('storage', onStorage)
      window.removeEventListener('mock_brief_card_changed', onStorage)
    }
  }, [])

  const loadMeetings = useCallback(async () => {
    setLoading(true)
    setFetchError(false)
    try {
      const [meetingsResponse, briefsResponse] = await Promise.all([
        getMeetings(),
        getUpcomingBriefs().catch(() => ({ briefs: [] })),
      ])

      setMeetings(meetingsResponse.meetings || [])
      setBriefs(briefsResponse.briefs || [])
    } catch (err) {
      showError(err)
      setFetchError(true)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadMeetings()

    const calendarError = localStorage.getItem('google_calendar_error')
    if (calendarError) {
      showError(calendarError)
      localStorage.removeItem('google_calendar_error')
    }
  }, [loadMeetings])

  const handleSyncCalendar = async () => {
    setSyncing(true)
    try {
      const result = await syncCalendar()
      if (result) {
        await loadMeetings()

        const syncedCount = result.synced_count || 0
        const skippedCount = result.skipped_count || 0
        const totalEvents = result.total_events || 0

        if (syncedCount > 0) {
          showSuccess(`Takvim senkronize edildi! ${syncedCount} toplantı eklendi.`)
        } else if (skippedCount > 0) {
          showSuccess(`${totalEvents} etkinlik bulundu, ancak ${skippedCount} tanesi toplantı linki olmadığı için atlandı.`)
        } else {
          showSuccess('Takvim senkronize edildi, ancak yeni toplantı bulunamadı.')
        }

        try {
          await startCalendarWatch()
        } catch (watchErr) {
          console.error('Could not enable calendar watch:', watchErr.message)
        }
      }
    } catch (err) {
      showError(err)
    } finally {
      setSyncing(false)
    }
  }

  const formatDate = (dateString) => {
    const date = new Date(dateString)
    return date.toLocaleDateString('tr-TR', {
      day: 'numeric',
      month: 'long',
      year: 'numeric',
      timeZone: 'Europe/Istanbul'
    })
  }

  const formatTime = (dateString) => {
    const date = new Date(dateString)
    return date.toLocaleTimeString('tr-TR', {
      hour: '2-digit',
      minute: '2-digit',
      timeZone: 'Europe/Istanbul'
    })
  }

  const handleRespond = async (meetingId, response) => {
    try {
      await respondMeeting(meetingId, response)
      await loadMeetings()
    } catch (err) {
      showError(err)
    }
  }

  const handleCancel = async (meetingId) => {
    try {
      const result = await cancelMeeting(meetingId)
      await loadMeetings()
      showSuccess('Toplantı dashboarddan kaldırıldı.')
      if (result?.calendar_warning) {
        showError(result.calendar_warning)
      }
    } catch (err) {
      showError(err)
    }
  }

  const now = new Date()

  const isActive = (m) => {
    const started = new Date(m.start_time) <= now
    const notEnded = new Date(m.end_time) >= now
    const hasRecentHeartbeat = m.last_heartbeat_at &&
      (now - new Date(m.last_heartbeat_at)) < 3 * 60 * 1000

    return started && (notEnded || hasRecentHeartbeat)
  }
  const isPending = (m) => m.user_response_status === 'needsAction' || m.user_response_status === 'tentative'

  const displayedMeetings = meetings

  const activeMeetings = displayedMeetings
    .filter(isActive)
    .sort((a, b) => new Date(a.start_time) - new Date(b.start_time))

  const pendingMeetings = displayedMeetings
    .filter(m => !isActive(m) && isPending(m) && new Date(m.start_time) > now)
    .sort((a, b) => new Date(a.start_time) - new Date(b.start_time))

  const upcomingMeetings = displayedMeetings
    .filter(m => !isActive(m) && !isPending(m) && new Date(m.end_time) > now)
    .sort((a, b) => new Date(a.start_time) - new Date(b.start_time))

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto w-full max-w-[1200px] px-6 py-12 md:px-10 md:py-16">
        {/* Sync action only — hero removed */}
        <div className="mb-12 flex justify-end">
          <button
            onClick={handleSyncCalendar}
            disabled={syncing}
            className="btn-primary inline-flex items-center gap-2"
          >
            <RefreshCw className={`w-4 h-4 ${syncing ? 'animate-spin' : ''}`} strokeWidth={1.5} />
            {syncing ? t('dashboard.syncing') : t('dashboard.syncCalendar')}
          </button>
        </div>

        {/* Active Meetings */}
        {activeMeetings.length > 0 && (
          <section className="mb-16">
            <div className="flex items-center gap-4 mb-8">
              <h2 className="font-serif text-h2 text-[var(--ink-charcoal)]">{t('dashboard.activeMeetings')}</h2>
              <span className="live-indicator">
                <span>{t('meeting.active')}</span>
              </span>
            </div>

            <div className="space-y-4">
              {activeMeetings.map((meeting) => (
                <article
                  key={meeting.id}
                  onClick={() => navigate(`/meetings/${meeting.id}`)}
                  className="group cursor-pointer p-8 md:p-10 border border-[var(--canvas-muted)] hover:border-[var(--ink-secondary)] transition-colors duration-150"
                >
                  <div className="flex items-start justify-between gap-6">
                    <div className="flex-1 min-w-0">
                      <StatusIndicator isActive={true} />
                      <h3 className="mt-3 font-serif text-h3 text-[var(--ink-charcoal)]">
                        {meeting.title}
                      </h3>
                      <div className="mt-3 flex items-center flex-wrap gap-5 text-meta text-text-secondary font-sans">
                        <span className="inline-flex items-center gap-2">
                          <Clock className="w-4 h-4" strokeWidth={1.5} />
                          {formatTime(meeting.start_time)} – {formatTime(meeting.end_time)}
                        </span>
                        {meeting.attendees && meeting.attendees.length > 0 && (
                          <span className="inline-flex items-center gap-2">
                            <Users className="w-4 h-4" strokeWidth={1.5} />
                            {meeting.attendees.length} katılımcı
                          </span>
                        )}
                      </div>
                    </div>

                    {meeting.meeting_url && (
                      <a
                        href={meeting.meeting_url}
                        target="_blank"
                        rel="noopener noreferrer"
                        onClick={(e) => e.stopPropagation()}
                        className="btn-primary inline-flex items-center gap-2 shrink-0"
                      >
                        <Play className="w-4 h-4" strokeWidth={1.5} />
                        {t('dashboard.joinNow')}
                      </a>
                    )}
                  </div>
                </article>
              ))}
            </div>
          </section>
        )}

        {/* Mock Brief */}
        {showMockBrief && <MockNextMeetingBriefCard />}

        {/* Pre-Meeting Briefs */}
        {briefs.length > 0 && (
          <section className="mb-16 border-t border-[var(--canvas-muted)] pt-16">
            <div className="flex items-center gap-3 mb-8">
              <FileText className="w-5 h-5 text-[var(--accent-ochre-ink)]" strokeWidth={1.5} />
              <h2 className="font-serif text-h2 text-[var(--ink-charcoal)]">Toplantı Hazırlıkları</h2>
            </div>
            <div className="space-y-4">
              {briefs.map((brief) => {
                const meeting = meetings.find(m => m.id === brief.target_meeting_id)
                return (
                  <BriefCard
                    key={brief.id}
                    brief={brief}
                    meetingTitle={meeting?.title || 'Toplantı'}
                    meetingTime={meeting?.start_time}
                  />
                )
              })}
            </div>
          </section>
        )}

        {/* Upcoming Meetings */}
        <section className="mb-16 border-t border-[var(--canvas-muted)] pt-16">
          <div className="flex items-center justify-between mb-8">
            <h2 className="font-serif text-h2 text-[var(--ink-charcoal)]">{t('dashboard.upcomingMeetings')}</h2>
            <button
              onClick={loadMeetings}
              disabled={loading}
              className="p-2 rounded-full text-text-secondary hover:text-[var(--ink-charcoal)] transition-colors"
              aria-label="Yenile"
            >
              <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} strokeWidth={1.5} />
            </button>
          </div>

          {loading ? (
            <LoadingState />
          ) : fetchError ? (
            <ErrorState message={t('dashboard.fetchError') || 'Toplantılar yüklenemedi.'} onRetry={loadMeetings} />
          ) : upcomingMeetings.length === 0 ? (
            <EmptyState
              icon={Calendar}
              title={t('dashboard.noUpcomingMeetings')}
              description={t('dashboard.syncCalendarHint') || 'Takviminizi senkronize edin.'}
            />
          ) : (
            <ul className="border-t border-[var(--canvas-muted)]">
              {upcomingMeetings.map((meeting) => (
                <li
                  key={meeting.id}
                  onClick={() => navigate(`/meetings/${meeting.id}`)}
                  className="group cursor-pointer px-1 py-6 border-b border-[var(--canvas-muted)] transition-colors hover:bg-[var(--canvas-muted)]/30"
                >
                  <div className="flex items-center justify-between gap-4">
                    <div className="flex-1 min-w-0">
                      <h3 className="font-serif text-h3 text-[var(--ink-charcoal)] truncate">
                        {meeting.title}
                      </h3>
                      <div className="mt-2 flex items-center flex-wrap gap-4 text-meta text-text-secondary font-sans">
                        <span className="inline-flex items-center gap-2">
                          <Clock className="w-4 h-4" strokeWidth={1.5} />
                          {formatDate(meeting.start_time)} · {formatTime(meeting.start_time)}
                        </span>
                        {meeting.attendees && meeting.attendees.length > 0 && (
                          <span className="inline-flex items-center gap-2">
                            <Users className="w-4 h-4" strokeWidth={1.5} />
                            {meeting.attendees.length}
                          </span>
                        )}
                      </div>
                    </div>

                    <div className="flex items-center gap-2 shrink-0" onClick={(e) => e.stopPropagation()}>
                      {meeting.meeting_url && (
                        <a
                          href={meeting.meeting_url}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="p-2 rounded-full text-text-secondary hover:text-[var(--ink-charcoal)] transition-colors"
                          aria-label="Toplantıya git"
                        >
                          <ExternalLink className="w-4 h-4" strokeWidth={1.5} />
                        </a>
                      )}
                      <button
                        onClick={() => handleCancel(meeting.id)}
                        className="p-2 rounded-full text-text-tertiary hover:text-danger transition-colors"
                        aria-label="Sil"
                      >
                        <Trash2 className="w-4 h-4" strokeWidth={1.5} />
                      </button>
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>

        {/* Pending Invites */}
        {pendingMeetings.length > 0 && (
          <section className="mb-16 border-t border-[var(--canvas-muted)] pt-16">
            <div className="flex items-center gap-3 mb-8">
              <h2 className="font-serif text-h2 text-[var(--ink-charcoal)]">{t('dashboard.pendingInvites')}</h2>
              <span className="badge">{pendingMeetings.length}</span>
            </div>

            <ul className="border-t border-[var(--canvas-muted)]">
              {pendingMeetings.map((meeting) => (
                <li
                  key={meeting.id}
                  className="px-1 py-6 border-b border-[var(--canvas-muted)]"
                >
                  <div className="flex items-start justify-between gap-4 mb-4">
                    <div>
                      <h3 className="font-serif text-h3 text-[var(--ink-charcoal)] mb-2">{meeting.title}</h3>
                      <div className="flex items-center gap-2 text-meta text-text-secondary font-sans">
                        <Clock className="w-4 h-4" strokeWidth={1.5} />
                        <span>{formatDate(meeting.start_time)} · {formatTime(meeting.start_time)}</span>
                      </div>
                    </div>
                    {meeting.meeting_url && (
                      <a
                        href={meeting.meeting_url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="p-2 rounded-full text-text-secondary hover:text-[var(--ink-charcoal)] transition-colors"
                      >
                        <ExternalLink className="w-4 h-4" strokeWidth={1.5} />
                      </a>
                    )}
                  </div>

                  <div className="flex flex-wrap gap-2">
                    <button
                      onClick={() => handleRespond(meeting.id, 'accepted')}
                      className="inline-flex items-center gap-2 h-9 px-4 rounded-full bg-[var(--ink-charcoal)] text-[var(--canvas-bone)] text-meta font-sans hover:bg-[var(--ink-secondary)] transition-colors"
                    >
                      <Check className="w-3.5 h-3.5" strokeWidth={1.5} /> {t('dashboard.accept')}
                    </button>
                    <button
                      onClick={() => handleRespond(meeting.id, 'tentative')}
                      className="inline-flex items-center gap-2 h-9 px-4 rounded-full border border-[var(--canvas-muted)] text-[var(--ink-charcoal)] text-meta font-sans hover:border-[var(--ink-secondary)] transition-colors"
                    >
                      <HelpCircle className="w-3.5 h-3.5" strokeWidth={1.5} /> {t('dashboard.maybe') || 'Belki'}
                    </button>
                    <button
                      onClick={() => handleRespond(meeting.id, 'declined')}
                      className="inline-flex items-center gap-2 h-9 px-4 rounded-full border border-danger/40 text-danger text-meta font-sans hover:border-danger transition-colors"
                    >
                      <XCircle className="w-3.5 h-3.5" strokeWidth={1.5} /> {t('dashboard.decline')}
                    </button>
                  </div>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </div>
  )
}
