import { useState, useEffect, useMemo } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  ArrowLeft, FileText,
  ExternalLink, Clock, MapPin, Users, Loader, AlertCircle,
  Check, X, Copy,
  List, CheckCircle, Sparkles, Pencil, Plus, Trash2
} from 'lucide-react'
import {
  getMeetingDetail,
  getFullTranscript,
  updateMeeting,
  updateMeetingChain,
  retryAutoDetectChain,
} from '../services/api'
import { getStoredUser } from '../services/auth'
import MarkdownRenderer from '../components/MarkdownRenderer'
import ChainBadge from '../components/ChainBadge'
import { showError, showSuccess } from '../utils/toast'

export default function MeetingDetails() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { t } = useTranslation()

  const [meeting, setMeeting] = useState(null)
  const [loading, setLoading] = useState(true)

  // Summary state
  const [summaryData, setSummaryData] = useState(null)

  // Copy state
  const [copiedField, setCopiedField] = useState(null)

  const handleCopySummary = async (text, field) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopiedField(field)
      setTimeout(() => setCopiedField(null), 2000)
    } catch (err) {
      console.error('Failed to copy:', err)
    }
  }

  // Faz 3: Group by Assignee State
  const [groupByAssignee, setGroupByAssignee] = useState(false)
  // Faz 4: Tag Filter State
  const [selectedTag, setSelectedTag] = useState(null)

  // Tabs State (Overview, Operations, Transcript)
  const [activeTab, setActiveTab] = useState('overview')
  const [transcript, setTranscript] = useState(null)

  // Edit Meeting Modal
  const [editMeetingModal, setEditMeetingModal] = useState({ open: false })
  const [editMeetingForm, setEditMeetingForm] = useState({})
  const [editMeetingLoading, setEditMeetingLoading] = useState(false)


  // Tabs will be defined after meeting status is computed

  // Build sorted attendees list: organizer first (even if not in attendees array), then rest
  const sortedAttendees = useMemo(() => {
    const attendees = meeting?.attendees || []
    const orgEmail = meeting?.organizer_email?.toLowerCase()
    if (!orgEmail) return attendees

    const isOrgInList = attendees.some(a => a.email?.toLowerCase() === orgEmail)
    const sorted = [...attendees].sort((a, b) => {
      const aIsOrg = a.email?.toLowerCase() === orgEmail ? -1 : 0
      const bIsOrg = b.email?.toLowerCase() === orgEmail ? -1 : 0
      return aIsOrg - bIsOrg
    })

    if (!isOrgInList) {
      sorted.unshift({ email: meeting.organizer_email, display_name: meeting.organizer_email })
    }
    return sorted
  }, [meeting?.attendees, meeting?.organizer_email])

  const structuredActionItems = useMemo(() => {
    const items = meeting?.action_items_structured || summaryData?.action_items_structured || []
    return Array.isArray(items) ? items : []
  }, [meeting?.action_items_structured, summaryData?.action_items_structured])

  useEffect(() => {
    loadMeeting()
  }, [id])

  // FIX 1.2: Use getMeetingDetail for structured data
  const loadMeeting = async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      // Use detail endpoint to get structured action_items and key_topics
      const foundMeeting = await getMeetingDetail(id);

      if (!foundMeeting) {
        showError('Toplantı bulunamadı')
        return
      }

      setMeeting(foundMeeting)

      if (foundMeeting.summary) {
        setSummaryData({ summary: foundMeeting.summary })
      }

      try {
        const transcriptData = await getFullTranscript(foundMeeting.id);
        setTranscript(transcriptData);
      } catch (e) {
        setTranscript(null);
      }
    } catch (err) {
      showError(err)
    } finally {
      if (!silent) setLoading(false)
    }
  }

  const formatDueDateLabel = (value) => {
    if (!value) return ''
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return ''
    return date.toLocaleString('tr-TR', {
      day: '2-digit',
      month: '2-digit',
      year: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      timeZone: 'Europe/Istanbul',
    })
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

  const getMeetingStatus = () => {
    if (!meeting) return 'unknown'
    const now = new Date()
    const start = new Date(meeting.start_time)
    const end = new Date(meeting.end_time)

    // Check if meeting has recent heartbeat (within 3 minutes)
    const hasRecentHeartbeat = meeting.last_heartbeat_at &&
      (now - new Date(meeting.last_heartbeat_at)) < 3 * 60 * 1000

    // Meeting is active if: within scheduled time OR has recent heartbeat
    if (now >= start && (now <= end || hasRecentHeartbeat)) return 'active'
    if (now < start) return 'upcoming'
    return 'past'
  }

  const getLastHeartbeatText = () => {
    if (!meeting?.last_heartbeat_at) return null
    const last = new Date(meeting.last_heartbeat_at)
    const now = new Date()
    const diffSeconds = Math.floor((now - last) / 1000)
    if (diffSeconds < 60) return 'Az önce'
    if (diffSeconds < 120) return '1 dk önce'
    if (diffSeconds < 180) return '2 dk önce'
    return `${Math.floor(diffSeconds / 60)} dk önce`
  }

  if (loading) {
    return (
      <div className="flex-1 p-8 flex items-center justify-center">
        <Loader className="w-8 h-8 animate-spin text-accent" />
      </div>
    )
  }

  if (!meeting) {
    return (
      <div className="flex-1 p-8">
        <div className="card text-center">
          <AlertCircle className="w-16 h-16 mx-auto mb-4 text-danger" />
          <p className="text-danger mb-4 font-sans">{t('meeting.notFound')}</p>
          <button onClick={() => navigate(-1)} className="btn-primary">
            <ArrowLeft className="w-4 h-4 mr-2" />
            {t('meeting.backToDashboard')}
          </button>
        </div>
      </div>
    )
  }

  const meetingStatus = getMeetingStatus()
  const isPastMeeting = meetingStatus === 'past'
  const isActiveMeeting = meetingStatus === 'active'
  const isUpcomingMeeting = meetingStatus === 'upcoming'

  // Ownership detection for edit
  const currentUser = getStoredUser()
  const isOwner = currentUser && meeting && currentUser.id === meeting.user_id

  const toLocalDatetimeString = (isoString) => {
    if (!isoString) return ''
    const d = new Date(isoString)
    const offset = d.getTimezoneOffset()
    const local = new Date(d.getTime() - offset * 60000)
    return local.toISOString().slice(0, 16)
  }

  const openEditMeetingModal = () => {
    setEditMeetingForm({
      title: meeting.title || '',
      startTime: toLocalDatetimeString(meeting.start_time),
      endTime: toLocalDatetimeString(meeting.end_time),
      description: meeting.description || '',
      location: meeting.location || '',
      attendees: (meeting.attendees || []).map(a => a.email),
      newAttendee: '',
      notifyAttendees: false,
    })
    setEditMeetingModal({ open: true })
  }

  const handleEditMeetingSubmit = async () => {
    setEditMeetingLoading(true)
    try {
      const payload = {}

      if (editMeetingForm.title !== (meeting.title || ''))
        payload.title = editMeetingForm.title
      if (editMeetingForm.startTime !== toLocalDatetimeString(meeting.start_time))
        payload.start_time = new Date(editMeetingForm.startTime).toISOString()
      if (editMeetingForm.endTime !== toLocalDatetimeString(meeting.end_time))
        payload.end_time = new Date(editMeetingForm.endTime).toISOString()
      if (editMeetingForm.description !== (meeting.description || ''))
        payload.description = editMeetingForm.description
      if (editMeetingForm.location !== (meeting.location || ''))
        payload.location = editMeetingForm.location

      const origAttendees = (meeting.attendees || []).map(a => a.email)
      const currentAttendees = editMeetingForm.attendees.filter(Boolean)
      if (JSON.stringify(currentAttendees) !== JSON.stringify(origAttendees))
        payload.attendees = currentAttendees

      if (Object.keys(payload).length === 0) {
        setEditMeetingModal({ open: false })
        return
      }

      // Notify: checkbox state is the source of truth
      // (time-change auto-enables it via onChange, but user can manually override)
      payload.send_updates = editMeetingForm.notifyAttendees ? 'all' : 'none'

      await updateMeeting(meeting.id, payload)
      setEditMeetingModal({ open: false })
      // Reload meeting data
      const data = await getMeetingDetail(id)
      setMeeting(data)
      showSuccess('Toplantı başarıyla güncellendi.')
    } catch (err) {
      showError(err)
    } finally {
      setEditMeetingLoading(false)
    }
  }


  // Design System (Tabs) - defined here because it depends on isActiveMeeting
    const tabs = [
    { id: 'overview', label: 'Genel Bakış' },
    { id: 'operations', label: 'Ana Konular & Aksiyonlar', hidden: isActiveMeeting || isUpcomingMeeting },
    { id: 'transcript', label: 'Tam Transkript', hidden: isActiveMeeting || isUpcomingMeeting || !transcript || transcript.length === 0 }
  ].filter(t => !t.hidden)

  return (
    <div className="flex-1 p-6 lg:p-8 overflow-y-auto min-h-screen">
      <div className="max-w-7xl mx-auto">
        {/* Header */}
        <div className="mb-8">
          <div className="flex flex-col md:flex-row md:items-end md:justify-between gap-4">
            <div>
              <button
                onClick={() => navigate(-1)}
                className="flex items-center gap-2 text-text-secondary hover:text-text mb-4 transition-colors font-serif"
              >
                <ArrowLeft className="w-5 h-5" />
                {t('meeting.backToMeetings')}
              </button>

              <h1 className="text-4xl text-text mb-2 font-serif">
                {meeting.title}
              </h1>
              <div className="flex items-center gap-4 text-text-secondary text-sm font-sans">
                <div className="flex items-center gap-2">
                  <Clock className="w-4 h-4" />
                  <span>
                    {formatDate(meeting.start_time)} • {formatTime(meeting.start_time)} - {formatTime(meeting.end_time)}
                  </span>
                </div>
              </div>

              {/* Chain assignment provenance + actions. Surfaces every
                  state of the v3 matcher (auto / suggestion / manual /
                  removed) so the user can audit and correct. Only one
                  source of truth — the badge owns the action menu. */}
              <div className="mt-3">
                <ChainBadge
                  chainId={meeting.chain_id}
                  chainType={meeting.chain_type}
                  chainAssignmentMethod={meeting.chain_assignment_method}
                  meetingId={meeting.id}
                  onRemoveChain={async (id) => {
                    try {
                      await updateMeetingChain(id, null)
                      setMeeting((prev) => ({
                        ...prev,
                        chain_id: null,
                        chain_type: null,
                        chain_assignment_method: 'manual_removed',
                      }))
                      showSuccess(t('chain.messages.removed'))
                    } catch (err) {
                      showError(err.message)
                    }
                  }}
                  onRetryAutoDetect={async (id) => {
                    try {
                      await retryAutoDetectChain(id)
                      setMeeting((prev) => ({
                        ...prev,
                        chain_assignment_method: null,
                        chain_assignment_locked_at: null,
                      }))
                      showSuccess(t('chain.messages.retried'))
                    } catch (err) {
                      showError(err.message)
                    }
                  }}
                />
              </div>
            </div>

            <div className="flex items-center gap-3">
              {meeting.meeting_url && !isPastMeeting && (
                <a
                  href={meeting.meeting_url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="btn-primary flex items-center gap-2"
                >
                  <ExternalLink className="w-4 h-4" />
                  Toplantıya Katıl
                </a>
              )}
              {isOwner && isUpcomingMeeting && (
                <button
                  onClick={openEditMeetingModal}
                  className="flex items-center gap-2 px-3 py-1.5 rounded-lg border border-[var(--canvas-muted)] bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)] hover:bg-[var(--canvas-muted)] text-xs font-serif transition-all"
                >
                  <Pencil className="w-3.5 h-3.5" />
                  Toplantıyı Düzenle
                </button>
              )}
              {isActiveMeeting && (
                <span className="px-3 py-1 bg-[var(--canvas-muted)] border border-[var(--canvas-muted)] rounded-full text-[var(--accent-ochre-ink)] text-xs flex items-center gap-1.5 font-serif">
                  <span className="w-2 h-2 bg-[var(--accent-ochre)] rounded-full animate-pulse"></span>
                  {t('meeting.inProgress')}
                </span>
              )}
              {isPastMeeting && (
                <span className="px-3 py-1 bg-surface rounded-full text-xs text-text-secondary font-serif">
                  {t('meeting.past')}
                </span>
              )}

            </div>
          </div>
        </div>

        {/* Tabs Navigation */}
        <div className="flex items-center gap-2 mb-8 border-b border-border">
          {tabs.map(tab => (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={`px-4 py-3 text-sm font-serif transition-all relative ${activeTab === tab.id
                ? 'text-accent'
                : 'text-text-secondary hover:text-text'
                }`}
            >
              {tab.label}
              {activeTab === tab.id && (
                <span className="absolute bottom-0 left-0 w-full h-0.5 bg-accent rounded-t-full"></span>
              )}
            </button>
          ))}
        </div>

        {/* TAB 1: OVERVIEW */}
        {activeTab === 'overview' && (
          <div className="space-y-8 animate-in fade-in slide-in-from-bottom-2 duration-300">

            {/* PHASE 4 — TRANSCRIPTION FAILED BANNER.
                Hides the review / summary / decisions sections below and
                shows a single friendly support message when the batch
                pipeline exhausted retries. Past-meetings only; active +
                upcoming don't render overview sections at all. */}
            {!isActiveMeeting && !isUpcomingMeeting && meeting.transcription_status === 'failed' ? (
              <div className="card border-[var(--accent-ochre-ink)]/40 bg-[var(--accent-ochre-ink)]/10">
                <h2 className="text-lg text-[var(--accent-ochre-ink)] flex items-center gap-2 font-serif mb-3">
                  <FileText className="w-5 h-5" />
                  Transkript oluşturulamadı
                </h2>
                <p className="text-text-secondary text-sm font-sans mb-2">
                  Ses dosyası teknik ekibimizce inceleniyor. Bu toplantı için özet ve kararlar şimdilik görüntülenmeyecek.
                </p>
                <p className="text-text-tertiary text-xs font-sans">
                  Destek için: <a href="mailto:support@syncrun.net" className="underline">support@syncrun.net</a>
                </p>
              </div>
            ) : (
              <>
                {/* DECISION REVIEW LINK */}
                {!isUpcomingMeeting && (
                  <div className="card border-[var(--canvas-muted)] bg-[var(--canvas-muted)]/40 flex items-center justify-between">
                    <div>
                      <h3 className="text-sm font-serif text-text">Kararlar ve Aksiyonlar</h3>
                      <p className="text-xs text-text-tertiary mt-1">Toplantıdan çıkarılan kararları gözden geçirin ve onaylayın</p>
                    </div>
                    <button
                      onClick={() => navigate(`/meetings/${meeting.id}/review`)}
                      className="px-4 py-2 text-sm bg-[var(--ink-charcoal)] text-[var(--canvas-bone)] rounded-md hover:bg-primary/90 transition-colors shrink-0"
                    >
                      Gözden Geçir →
                    </button>
                  </div>
                )}

            {/* MEETING SUMMARY - HIDE FOR ACTIVE AND UPCOMING MEETINGS */}
            {!isActiveMeeting && !isUpcomingMeeting && (
              <div className="grid grid-cols-1 lg:grid-cols-3 gap-8">
                {meeting.executive_summary && (
                  <div className="lg:col-span-3 card border-[var(--canvas-muted)] bg-[var(--canvas-muted)]">
                    <div className="flex items-center justify-between mb-3">
                      <h2 className="text-lg text-[var(--accent-ochre-ink)] flex items-center gap-2 font-serif">
                        <Sparkles className="w-5 h-5" />
                        Yönetici Özeti
                      </h2>
                      <button
                        onClick={() => handleCopySummary(meeting.executive_summary, 'executive')}
                        className="flex items-center gap-1.5 px-3 py-1.5 text-sm text-text-secondary hover:text-text bg-surface hover:bg-surface-hover rounded-lg transition-colors"
                        title="Özeti kopyala"
                      >
                        {copiedField === 'executive' ? <Check className="w-4 h-4 text-[var(--accent-ochre-ink)]" /> : <Copy className="w-4 h-4" />}
                        {copiedField === 'executive' ? 'Kopyalandı' : 'Kopyala'}
                      </button>
                    </div>
                    <div className="text-text-secondary leading-relaxed text-base font-sans">
                      <MarkdownRenderer content={meeting.executive_summary} />
                    </div>
                  </div>
                )}

                <div className="lg:col-span-2 space-y-6">
                  <div className="card">
                    <div className="flex items-center justify-between mb-6">
                      <h2 className="text-xl text-text flex items-center gap-2 font-serif">
                        <FileText className="w-5 h-5 text-text-secondary" />
                        Toplantı Özeti
                      </h2>
                      {meeting.summary && (
                        <button
                          onClick={() => handleCopySummary(meeting.summary, 'summary')}
                          className="flex items-center gap-1.5 px-3 py-1.5 text-sm text-text-secondary hover:text-text bg-surface hover:bg-surface-hover rounded-lg transition-colors"
                          title="Özeti kopyala"
                        >
                          {copiedField === 'summary' ? <Check className="w-4 h-4 text-[var(--accent-ochre-ink)]" /> : <Copy className="w-4 h-4" />}
                          {copiedField === 'summary' ? 'Kopyalandı' : 'Kopyala'}
                        </button>
                      )}
                    </div>

                    {meeting.summary ? (
                      <div className="prose prose-invert max-w-none prose-p:text-text-secondary prose-headings:text-text prose-strong:text-text prose-li:text-text-secondary font-sans">
                        <MarkdownRenderer content={meeting.summary} />
                      </div>
                    ) : (
                      <div className="text-center py-12 bg-surface rounded-lg border border-border border-dashed">
                        <FileText className="w-12 h-12 text-text-tertiary mx-auto mb-3" />
                        <p className="text-text-secondary mb-4 font-sans">Henüz bir özet oluşturulmadı.</p>
                      </div>
                    )}
                  </div>
                </div>

                <div className="space-y-6">
                  <div className="card">
                    <h2 className="text-sm text-text-secondary mb-4 uppercase tracking-wider font-serif">Detaylar</h2>
                    {meeting.location && (
                      <div className="flex items-start gap-3 mb-4">
                        <MapPin className="w-4 h-4 text-text-tertiary mt-1" />
                        <div>
                          <div className="text-text-secondary text-sm font-sans">{meeting.location}</div>
                        </div>
                      </div>
                    )}

                    {sortedAttendees.length > 0 && (
                      <div className="flex items-start gap-3">
                        <Users className="w-4 h-4 text-text-tertiary mt-1" />
                        <div className="flex-1">
                          <div className="text-text-secondary text-sm mb-2 font-serif">
                            Katılımcılar ({sortedAttendees.length})
                          </div>
                          <div className="space-y-2">
                            {sortedAttendees.map((attendee, idx) => {
                              const isOrganizer = meeting.organizer_email && attendee.email && attendee.email.toLowerCase() === meeting.organizer_email.toLowerCase()
                              return (
                                <div key={idx} className="flex items-center gap-2 text-sm text-text-secondary font-sans">
                                  <div className={`w-2 h-2 rounded-full ${isOrganizer ? 'bg-accent' : 'bg-[rgba(132,145,164,0.3)]'}`}></div>
                                  {attendee.display_name || attendee.email}
                                  {isOrganizer && (
                                    <span className="text-xs text-accent opacity-70">(Toplantı Sahibi)</span>
                                  )}
                                </div>
                              )
                            })}
                          </div>
                        </div>
                      </div>
                    )}
                  </div>

                  {/* Bot Status Card removed — desktop-only soft-hide.
                      Bot status fields stay on the meeting payload but
                      no longer surface in the UI. */}
                </div>
              </div>
            )}

            {/* ACTIVE MEETING LAYOUT — bot status card removed
                (desktop-only soft-hide). Only the meeting-details card
                remains; the grid collapses to a single column. */}
            {isActiveMeeting && (
              <div className="grid grid-cols-1 gap-8">
                <div className="card">
                  <h2 className="text-sm text-text-secondary mb-4 uppercase tracking-wider font-serif">Detaylar</h2>
                  {meeting.location && (
                    <div className="flex items-start gap-3 mb-4">
                      <MapPin className="w-4 h-4 text-text-tertiary mt-1" />
                      <div>
                        <div className="text-text-secondary text-sm font-sans">{meeting.location}</div>
                      </div>
                    </div>
                  )}

                  {sortedAttendees.length > 0 && (
                    <div className="flex items-start gap-3">
                      <Users className="w-4 h-4 text-text-tertiary mt-1" />
                      <div className="flex-1">
                        <div className="text-text-secondary text-sm mb-2 font-serif">
                          Katılımcılar ({sortedAttendees.length})
                        </div>
                        <div className="space-y-2">
                          {sortedAttendees.map((attendee, idx) => {
                            const isOrganizer = meeting.organizer_email && attendee.email && attendee.email.toLowerCase() === meeting.organizer_email.toLowerCase()
                            return (
                              <div key={idx} className="flex items-center gap-2 text-sm text-text-secondary font-sans">
                                <div className={`w-2 h-2 rounded-full ${isOrganizer ? 'bg-accent' : 'bg-[rgba(132,145,164,0.3)]'}`}></div>
                                {attendee.display_name || attendee.email}
                                {isOrganizer && (
                                  <span className="text-xs text-accent opacity-70">(Toplantı Sahibi)</span>
                                )}
                              </div>
                            )
                          })}
                        </div>
                      </div>
                    </div>
                  )}
                </div>
              </div>
            )}

            {/* UPCOMING MEETING LAYOUT — bot status card removed
                (desktop-only soft-hide). Only the meeting-details card
                remains; the grid collapses to a single column. */}
            {isUpcomingMeeting && (
              <div className="grid grid-cols-1 gap-8">
                <div className="card">
                  <h2 className="text-sm text-text-secondary mb-4 uppercase tracking-wider font-serif">Detaylar</h2>
                  {meeting.location && (
                    <div className="flex items-start gap-3 mb-4">
                      <MapPin className="w-4 h-4 text-text-tertiary mt-1" />
                      <div>
                        <div className="text-text-secondary text-sm font-sans">{meeting.location}</div>
                      </div>
                    </div>
                  )}

                  {sortedAttendees.length > 0 && (
                    <div className="flex items-start gap-3">
                      <Users className="w-4 h-4 text-text-tertiary mt-1" />
                      <div className="flex-1">
                        <div className="text-text-secondary text-sm mb-2 font-serif">
                          Katılımcılar ({sortedAttendees.length})
                        </div>
                        <div className="space-y-2">
                          {sortedAttendees.map((attendee, idx) => {
                            const isOrganizer = meeting.organizer_email && attendee.email && attendee.email.toLowerCase() === meeting.organizer_email.toLowerCase()
                            return (
                              <div key={idx} className="flex items-center gap-2 text-sm text-text-secondary font-sans">
                                <div className={`w-2 h-2 rounded-full ${isOrganizer ? 'bg-accent' : 'bg-[rgba(132,145,164,0.3)]'}`}></div>
                                {attendee.display_name || attendee.email}
                                {isOrganizer && (
                                  <span className="text-xs text-accent opacity-70">(Toplantı Sahibi)</span>
                                )}
                              </div>
                            )
                          })}
                        </div>
                      </div>
                    </div>
                  )}
                </div>
              </div>
            )}
              </>
            )}
          </div>
        )}

        {/* TAB 2: OPERATIONS - Faz 4 UX Enhancements */}
        {activeTab === 'operations' && (
          <div className="animate-in fade-in slide-in-from-bottom-2 duration-300">
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-8">
              {/* LEFT COLUMN: Topic Timeline View */}
              <div className="space-y-6">
                {(() => {
                  const topicsStructured = meeting.key_topics_structured || summaryData?.key_topics_structured || [];
                  const topicsLegacy = meeting.key_topics || summaryData?.key_topics || [];
                  const hasTopics = topicsStructured.length > 0 || topicsLegacy.length > 0;

                  return hasTopics ? (
                    <div className="card h-full">
                      <h2 className="text-xl text-text mb-6 flex items-center gap-2 font-serif">
                        <List className="w-5 h-5 text-[var(--accent-ochre-ink)]" />
                        Ana Konular
                      </h2>
                      <div className="space-y-3">
                        {(topicsStructured.length > 0 ? topicsStructured : topicsLegacy.map(t => ({ topic: t }))).map((topicData, idx) => {
                          const topic = typeof topicData === 'string' ? topicData : topicData.topic;

                          return (
                            <div
                              key={idx}
                              className="p-4 bg-surface rounded-lg border border-border hover:bg-[var(--canvas-muted)]/50 hover:border-[var(--ink-secondary)] transition-all cursor-pointer group"
                            >
                              <div className="flex items-start justify-between gap-3">
                                <div className="flex items-start gap-3">
                                  {/* Timeline indicator */}
                                  <div className="flex flex-col items-center">
                                    <div className="w-3 h-3 rounded-full bg-[rgba(132,145,164,0.3)] border-2 border-secondary group-hover:bg-[var(--accent-ochre)] transition-colors"></div>
                                    {idx < (topicsStructured.length > 0 ? topicsStructured.length : topicsLegacy.length) - 1 && (
                                      <div className="w-0.5 h-8 bg-border mt-1"></div>
                                    )}
                                  </div>
                                  <div>
                                    <p className="text-text-secondary text-sm font-serif">{topic}</p>
                                  </div>
                                </div>
                                {/* Topic index badge */}
                                <span className="text-[10px] px-2 py-0.5 bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)] rounded font-mono">
                                  #{idx + 1}
                                </span>
                              </div>
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  ) : (
                    <div className="card h-full flex flex-col items-center justify-center py-12 text-text-tertiary bg-surface border-dashed font-sans">
                      <List className="w-8 h-8 mb-3 opacity-20" />
                      Bu toplantı için henüz konu çıkarılmadı.
                    </div>
                  );
                })()}
              </div>

              {/* RIGHT COLUMN: Action Items with Metadata */}
              <div className="space-y-6">
                <div className="card h-full">
                  <div className="flex items-center justify-between mb-6">
                    <h2 className="text-xl text-text flex items-center gap-2 font-serif">
                      <CheckCircle className="w-5 h-5 text-[var(--accent-ochre-ink)]" />
                      Aksiyonlar
                    </h2>
                    <div className="flex items-center gap-2">
                      <button
                        onClick={() => setGroupByAssignee(!groupByAssignee)}
                        className={`flex items-center gap-2 px-3 py-1.5 rounded-lg border transition-all text-xs font-serif ${groupByAssignee
                          ? 'bg-[var(--canvas-muted)] border-[var(--canvas-muted)] text-[var(--accent-ochre-ink)] hover:bg-[var(--canvas-muted)]'
                          : 'bg-surface border-border text-text-secondary hover:bg-[var(--canvas-muted)]/50'
                          }`}
                      >
                        <Users className="w-3.5 h-3.5" />
                        {groupByAssignee ? 'Listeye Dön' : 'Kişiye Göre Grupla'}
                      </button>
                    </div>
                  </div>

                  {(() => {
                    const actionsStructured = meeting.action_items_structured || summaryData?.action_items_structured || [];
                    const actionsLegacy = meeting.action_items || summaryData?.action_items || [];
                    const hasActions = actionsStructured.length > 0 || actionsLegacy.length > 0;
                    const topicsStructured = meeting.key_topics_structured || summaryData?.key_topics_structured || [];
                    const topicsLegacy = meeting.key_topics || summaryData?.key_topics || [];

                    // Priority styles (Border & Badge)
                    const priorityStyles = {
                      high: {
                        border: 'border-l-danger',
                        badge: 'bg-danger/10 text-danger border-danger/40',
                        label: 'Yüksek Öncelik'
                      },
                      medium: {
                        border: 'border-l-accent',
                        badge: 'bg-[var(--canvas-muted)] text-accent border-[var(--canvas-muted)]',
                        label: 'Orta Öncelik'
                      },
                      low: {
                        border: 'border-l-border',
                        badge: 'bg-[var(--canvas-muted)] text-text-secondary border-border',
                        label: 'Düşük Öncelik'
                      }
                    };

                    // Faz 4: Tag Extraction & Filtering
                    let allTags = {};
                    actionsStructured.forEach(item => {
                      if (item.tags && Array.isArray(item.tags)) {
                        item.tags.forEach(tag => {
                          const normalizedTag = tag.toLowerCase().trim();
                          allTags[normalizedTag] = (allTags[normalizedTag] || 0) + 1;
                        });
                      }
                    });

                    const sortedTags = Object.entries(allTags).sort((a, b) => b[1] - a[1]); // Sort by frequency
                    const hasTags = sortedTags.length > 0;

                    // Filter Actions
                    const actionsDisplay = selectedTag
                      ? actionsStructured.filter(a => a.tags && a.tags.some(t => t.toLowerCase().trim() === selectedTag))
                      : (actionsStructured.length > 0 ? actionsStructured : actionsLegacy.map(a => ({ description: a, assignee_name: null })));

                    const hasFilteredActions = actionsDisplay.length > 0;

                    return hasActions ? (
                      <>
                        {/* Tag Filter Bar */}
                        {hasTags && (
                          <div className="flex items-center gap-2 mb-4 overflow-x-auto pb-2 scrollbar-hide">
                            <button
                              onClick={() => setSelectedTag(null)}
                              className={`px-2 py-1 rounded text-[10px] transition-colors border font-serif ${!selectedTag
                                ? 'bg-accent text-text border-accent'
                                : 'bg-surface text-text-secondary border-border hover:bg-[var(--canvas-muted)]/50'}`}
                            >
                              Tümü ({actionsStructured.length})
                            </button>
                            {sortedTags.map(([tag, count]) => (
                              <button
                                key={tag}
                                onClick={() => setSelectedTag(selectedTag === tag ? null : tag)}
                                className={`px-2 py-1 rounded text-[10px] transition-colors border flex items-center gap-1 whitespace-nowrap font-serif ${selectedTag === tag
                                  ? 'bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)] border-[var(--ink-secondary)]'
                                  : 'bg-surface text-text-secondary border-border hover:bg-[var(--canvas-muted)]/50 hover:border-border'}`}
                              >
                                #{tag}
                                <span className="opacity-60 text-[9px] ml-0.5">({count})</span>
                              </button>
                            ))}
                          </div>
                        )}

                        {hasFilteredActions ? (
                          groupByAssignee ? (
                            /* GROUPED VIEW */
                            <div className="space-y-6">
                              {Object.entries(
                                actionsDisplay.reduce((groups, item) => {
                                  const assignee = (typeof item === 'object' ? item.assignee_name : null) || 'Belirsiz';
                                  if (!groups[assignee]) groups[assignee] = [];
                                  groups[assignee].push(item);
                                  return groups;
                                }, {})
                              ).map(([assignee, items], gIdx) => (
                                <div key={gIdx} className="space-y-3">
                                  <div className="flex items-center gap-2 text-sm text-text-secondary ml-1 font-serif">
                                    <Users className="w-4 h-4 text-text-tertiary" />
                                    {assignee}
                                    <span className="text-xs text-text-tertiary font-sans">({items.length})</span>
                                  </div>
                                  <div className="space-y-3 pl-2 border-l-2 border-border">
                                    {items.map((item, idx) => {
                                      const isStructured = typeof item === 'object' && item.description;
                                      const description = isStructured ? item.description : item;
                                      const dueDate = isStructured ? item.due_date : null;
                                      const topicIds = isStructured ? item.topic_ids : [];
                                      const priority = isStructured ? (item.priority || 'medium') : 'medium';
                                      const pStyle = priorityStyles[priority] || priorityStyles.medium;
                                      const isOptional = isStructured ? (item.is_optional || false) : false;
                                      const tags = isStructured ? (item.tags || []) : [];

                                      return (
                                        <div key={idx} className={`p-3 bg-surface rounded-lg border border-border ${isOptional ? 'opacity-60 grayscale-[0.5]' : ''}`}>
                                          <div className="flex items-start gap-3">
                                            <div className={`flex-shrink-0 mt-0.5 w-4 h-4 rounded-full flex items-center justify-center ${isOptional ? 'bg-[var(--canvas-muted)] text-text-secondary' : 'bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)]'
                                              }`}>
                                              <Check className="w-2.5 h-2.5" />
                                            </div>
                                            <div className="flex-1 min-w-0">
                                              <span className={`text-sm font-sans ${isOptional ? 'italic text-text-tertiary' : 'text-text-secondary'}`}>{description}</span>
                                              <div className="flex flex-wrap items-center gap-2 mt-1.5">
                                                {isOptional && <span className="text-[10px] px-1.5 py-0.5 bg-[var(--canvas-muted)] text-text-secondary rounded border border-border font-serif">Opsiyonel</span>}
                                                {!isOptional && <span className={`text-[10px] px-1.5 py-0.5 rounded border ${pStyle.badge} font-serif`}>{pStyle.label}</span>}
                                                {dueDate && <span className="text-[10px] px-1.5 py-0.5 bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)] rounded border border-[var(--canvas-muted)] flex items-center gap-1 font-serif"><Clock className="w-2 h-2" />{formatDueDateLabel(dueDate)}</span>}

                                                {/* Tags */}
                                                {tags.map((tag, tIdx) => (
                                                  <span
                                                    key={tIdx}
                                                    onClick={(e) => { e.stopPropagation(); setSelectedTag(tag.toLowerCase().trim()); }}
                                                    className="text-[10px] px-1.5 py-0.5 bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)] rounded border border-[var(--canvas-muted)] cursor-pointer hover:bg-[var(--canvas-muted)] font-serif"
                                                  >
                                                    #{tag}
                                                  </span>
                                                ))}
                                              </div>
                                            </div>
                                          </div>
                                        </div>
                                      );
                                    })}
                                  </div>
                                </div>
                              ))}
                            </div>
                          ) : (
                            /* LIST VIEW (Existing) */
                            <div className="space-y-4">
                              {actionsDisplay.map((item, idx) => {
                                const isStructured = typeof item === 'object' && item.description;
                                const description = isStructured ? item.description : item;
                                const assignee = isStructured ? item.assignee_name : null;
                                const dueDate = isStructured ? item.due_date : null;
                                const topicIds = isStructured ? item.topic_ids : [];
                                const priority = isStructured ? (item.priority || 'medium') : 'medium';
                                const pStyle = priorityStyles[priority] || priorityStyles.medium;
                                const isOptional = isStructured ? (item.is_optional || false) : false;
                                const tags = isStructured ? (item.tags || []) : [];

                                return (
                                  <div key={idx} className={`p-4 bg-surface rounded-lg border-y border-r border-border border-l-[3px] ${pStyle.border} ${isOptional ? 'opacity-60 bg-surface grayscale-[0.5]' : 'hover:bg-[var(--canvas-muted)]/50'} transition-all`}>
                                    <div className="flex items-start gap-3">
                                      <div className={`flex-shrink-0 mt-0.5 w-5 h-5 rounded-full flex items-center justify-center ${isOptional ? 'bg-[var(--canvas-muted)] border border-border' : 'bg-[var(--canvas-muted)] border border-[var(--canvas-muted)]'
                                        }`}>
                                        <Check className={`w-3 h-3 ${isOptional ? 'text-text-secondary' : 'text-[var(--accent-ochre-ink)]'}`} />
                                      </div>

                                      <div className="flex-1 min-w-0">
                                        {/* Description */}
                                        <span className={`text-sm leading-relaxed font-sans ${isOptional ? 'italic text-text-tertiary' : 'text-text-secondary'}`}>{description}</span>

                                        {/* Metadata row */}
                                        <div className="flex flex-wrap items-center gap-2 mt-2">

                                          {/* Optional Badge */}
                                          {isOptional && (
                                            <span className="text-[10px] px-2 py-0.5 bg-[var(--canvas-muted)] text-text-secondary rounded border border-border inline-flex items-center gap-1 font-serif">
                                              Opsiyonel
                                            </span>
                                          )}

                                          {/* Priority Badge */}
                                          {!isOptional && (
                                            <span className={`text-[10px] px-2 py-0.5 rounded border inline-flex items-center gap-1 font-serif ${pStyle.badge}`}>
                                              {pStyle.label}
                                            </span>
                                          )}

                                          {/* Assignee with confidence */}
                                          {assignee && (
                                            <span className="text-[10px] px-2 py-0.5 rounded border inline-flex items-center gap-1 font-serif bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)] border-[var(--canvas-muted)]">
                                              <Users className="w-2.5 h-2.5" />
                                              {assignee}
                                            </span>
                                          )}

                                          {dueDate && (
                                            <span className="text-[10px] px-2 py-0.5 bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)] rounded border border-[var(--canvas-muted)] inline-flex items-center gap-1 font-serif">
                                              <Clock className="w-2.5 h-2.5" />
                                              {formatDueDateLabel(dueDate)}
                                            </span>
                                          )}

                                          {/* Topic badges */}
                                          {topicIds && topicIds.length > 0 && (
                                            topicIds.map((topicId, tidx) => {
                                              const matchedTopic = topicsStructured.find(t => t.id === topicId);
                                              const topicName = matchedTopic?.topic || `Konu`;
                                              if (!matchedTopic) return null;
                                              return (
                                                <span key={tidx} className="text-[10px] px-2 py-0.5 bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)] rounded border border-[var(--canvas-muted)] font-serif">
                                                  📌 {topicName.length > 20 ? topicName.substring(0, 20) + '...' : topicName}
                                                </span>
                                              );
                                            })
                                          )}

                                          {/* Tags */}
                                          {tags.map((tag, tIdx) => (
                                            <span
                                              key={tIdx}
                                              onClick={(e) => { e.stopPropagation(); setSelectedTag(tag.toLowerCase().trim()); }}
                                              className="text-[10px] px-2 py-0.5 bg-[var(--canvas-muted)] text-[var(--accent-ochre-ink)] rounded border border-[var(--canvas-muted)] cursor-pointer hover:bg-[var(--canvas-muted)] font-serif"
                                            >
                                              #{tag}
                                            </span>
                                          ))}
                                        </div>
                                      </div>
                                    </div>
                                  </div>
                                );
                              })}
                            </div>
                          )
                        ) : (
                          <div className="text-center py-12 text-text-tertiary font-sans">
                            {selectedTag ? `"${selectedTag}" etiketine ait aksiyon bulunamadı.` : 'Bu toplantı için henüz bir aksiyon maddesi çıkarılmadı.'}
                          </div>
                        )}
                      </>
                    ) : (
                      <div className="text-center py-12 text-text-tertiary font-sans">
                        Bu toplantı için henüz bir aksiyon maddesi çıkarılmadı.
                      </div>
                    );

                  })()}
                </div >
              </div>
            </div>
          </div>
        )}





        {/* Edit Meeting Modal */}
        {editMeetingModal.open && (
          <div className="fixed inset-0 z-50 flex items-center justify-center bg-[rgba(45,43,39,0.4)]">
            <div className="bg-surface border border-border rounded-none p-8 w-full max-w-lg max-h-[90vh] overflow-y-auto">
              <div className="flex items-center justify-between mb-5">
                <h3 className="text-lg font-serif text-text">Toplantıyı Düzenle</h3>
                <button onClick={() => setEditMeetingModal({ open: false })} className="text-text-secondary hover:text-text">
                  <X className="w-5 h-5" />
                </button>
              </div>

              <div className="space-y-4">
                {/* Title */}
                <div>
                  <label className="block text-xs text-text-secondary font-serif mb-1">Başlık</label>
                  <input
                    type="text"
                    value={editMeetingForm.title}
                    onChange={e => setEditMeetingForm(f => ({ ...f, title: e.target.value }))}
                    className="w-full px-3 py-2 rounded-lg bg-background border border-border text-text text-sm font-sans focus:outline-none focus:border-secondary"
                  />
                </div>

                {/* Start / End Time */}
                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <label className="block text-xs text-text-secondary font-serif mb-1">Başlangıç</label>
                    <input
                      type="datetime-local"
                      value={editMeetingForm.startTime}
                      onChange={e => setEditMeetingForm(f => ({ ...f, startTime: e.target.value, notifyAttendees: true }))}
                      className="w-full px-3 py-2 rounded-lg bg-background border border-border text-text text-sm font-sans focus:outline-none focus:border-secondary"
                    />
                  </div>
                  <div>
                    <label className="block text-xs text-text-secondary font-serif mb-1">Bitiş</label>
                    <input
                      type="datetime-local"
                      value={editMeetingForm.endTime}
                      onChange={e => setEditMeetingForm(f => ({ ...f, endTime: e.target.value, notifyAttendees: true }))}
                      className="w-full px-3 py-2 rounded-lg bg-background border border-border text-text text-sm font-sans focus:outline-none focus:border-secondary"
                    />
                  </div>
                </div>

                {/* Description */}
                <div>
                  <label className="block text-xs text-text-secondary font-serif mb-1">Açıklama</label>
                  <textarea
                    value={editMeetingForm.description}
                    onChange={e => setEditMeetingForm(f => ({ ...f, description: e.target.value }))}
                    rows={2}
                    className="w-full px-3 py-2 rounded-lg bg-background border border-border text-text text-sm font-sans focus:outline-none focus:border-secondary resize-none"
                  />
                </div>

                {/* Location */}
                <div>
                  <label className="block text-xs text-text-secondary font-serif mb-1">Konum</label>
                  <input
                    type="text"
                    value={editMeetingForm.location}
                    onChange={e => setEditMeetingForm(f => ({ ...f, location: e.target.value }))}
                    className="w-full px-3 py-2 rounded-lg bg-background border border-border text-text text-sm font-sans focus:outline-none focus:border-secondary"
                  />
                </div>

                {/* Attendees */}
                <div>
                  <label className="block text-xs text-text-secondary font-serif mb-1">Katılımcılar</label>
                  <div className="space-y-1.5">
                    {editMeetingForm.attendees?.map((email, idx) => (
                      <div key={idx} className="flex items-center gap-2">
                        <input
                          type="email"
                          value={email}
                          onChange={e => {
                            const updated = [...editMeetingForm.attendees]
                            updated[idx] = e.target.value
                            setEditMeetingForm(f => ({ ...f, attendees: updated }))
                          }}
                          className="flex-1 px-3 py-1.5 rounded-lg bg-background border border-border text-text text-sm font-sans focus:outline-none focus:border-secondary"
                        />
                        <button
                          type="button"
                          onClick={() => setEditMeetingForm(f => ({ ...f, attendees: f.attendees.filter((_, i) => i !== idx) }))}
                          className="text-text-tertiary hover:text-danger transition-colors"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    ))}
                    <div className="flex items-center gap-2">
                      <input
                        type="email"
                        value={editMeetingForm.newAttendee || ''}
                        onChange={e => setEditMeetingForm(f => ({ ...f, newAttendee: e.target.value }))}
                        onKeyDown={e => {
                          if (e.key === 'Enter' && editMeetingForm.newAttendee?.trim()) {
                            e.preventDefault()
                            setEditMeetingForm(f => ({ ...f, attendees: [...f.attendees, f.newAttendee.trim()], newAttendee: '' }))
                          }
                        }}
                        placeholder="yeni@example.com"
                        className="flex-1 px-3 py-1.5 rounded-lg bg-background border border-border text-text text-sm font-sans focus:outline-none focus:border-secondary"
                      />
                      <button
                        type="button"
                        onClick={() => {
                          if (editMeetingForm.newAttendee?.trim()) {
                            setEditMeetingForm(f => ({ ...f, attendees: [...f.attendees, f.newAttendee.trim()], newAttendee: '' }))
                          }
                        }}
                        className="text-text-tertiary hover:text-[var(--accent-ochre-ink)] transition-colors"
                      >
                        <Plus className="w-4 h-4" />
                      </button>
                    </div>
                  </div>
                </div>

                {/* Meeting URL (read-only) */}
                {meeting.meeting_url && (
                  <div>
                    <label className="block text-xs text-text-secondary font-serif mb-1">Toplantı Bağlantısı (değiştirilemez)</label>
                    <input
                      type="text"
                      value={meeting.meeting_url}
                      disabled
                      className="w-full px-3 py-2 rounded-lg bg-background/50 border border-border text-text-secondary text-sm font-sans cursor-not-allowed opacity-60"
                    />
                  </div>
                )}

                {/* Notify Attendees Checkbox */}
                <label className="flex items-center gap-2 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={editMeetingForm.notifyAttendees || false}
                    onChange={e => setEditMeetingForm(f => ({ ...f, notifyAttendees: e.target.checked }))}
                    className="rounded border-border"
                  />
                  <span className="text-sm text-text font-sans">Katılımcıları bilgilendir</span>
                  <span className="text-xs text-text-tertiary font-sans">(tarih/saat değişikliğinde otomatik açılır)</span>
                </label>
              </div>

              {/* Actions */}
              <div className="flex justify-end gap-3 mt-6 pt-4 border-t border-border">
                <button
                  onClick={() => setEditMeetingModal({ open: false })}
                  disabled={editMeetingLoading}
                  className="px-4 py-2 rounded-lg border border-border text-sm text-text-secondary hover:bg-[var(--canvas-muted)]/50 font-serif"
                >
                  Vazgeç
                </button>
                <button
                  onClick={handleEditMeetingSubmit}
                  disabled={editMeetingLoading}
                  className="px-4 py-2 rounded-lg border border-[var(--canvas-muted)] bg-[var(--canvas-muted)] text-sm text-[var(--accent-ochre-ink)] hover:bg-[var(--canvas-muted)] font-serif flex items-center gap-2"
                >
                  {editMeetingLoading && <Loader className="w-3.5 h-3.5 animate-spin" />}
                  {editMeetingLoading ? 'Kaydediliyor...' : 'Değişiklikleri Kaydet'}
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
    </div >
  )
}
