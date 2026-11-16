import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Calendar, Clock, RefreshCw, ExternalLink, FileText } from 'lucide-react'
import { getMeetings } from '../services/api'

export default function PastMeetings() {
  const navigate = useNavigate()
  const { t } = useTranslation()
  const [meetings, setMeetings] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    loadPast()
  }, [])

  const loadPast = async () => {
    setLoading(true)
    setError('')
    try {
      const data = await getMeetings('past')
      const now = new Date()

      const filterPast = (list) => (list || []).filter(m => new Date(m.end_time) < now)
        .sort((a, b) => new Date(b.end_time) - new Date(a.end_time))

      setMeetings(filterPast(data.meetings || []))
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  const formatDate = (dateString) => {
    const date = new Date(dateString)
    return date.toLocaleDateString('tr-TR', { timeZone: 'Europe/Istanbul' })
  }

  const formatTime = (dateString) => {
    const date = new Date(dateString)
    return date.toLocaleTimeString('tr-TR', {
      hour: '2-digit',
      minute: '2-digit',
      timeZone: 'Europe/Istanbul'
    })
  }

  const displayedMeetings = meetings

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto w-full max-w-[1200px] px-6 py-16 md:px-10 md:py-24">
        {/* Header */}
        <div className="mb-12 flex items-start justify-between gap-6">
          <div>
            <span className="eyebrow">Arşiv</span>
            <h1 className="mt-4 font-serif text-h1 text-[var(--ink-charcoal)]">
              {t('pastMeetings.title')}
            </h1>
            <p className="mt-3 text-body text-text-secondary font-sans leading-[1.55]">
              {t('pastMeetings.subtitle') || 'Son 30 günün tamamlanan toplantıları.'}
            </p>
          </div>
          <button
            onClick={loadPast}
            disabled={loading}
            className="btn-secondary inline-flex items-center gap-2 shrink-0"
          >
            <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} strokeWidth={1.5} />
            {t('common.refresh') || 'Yenile'}
          </button>
        </div>

        {error && (
          <div className="mb-8 p-4 border border-danger/40 text-danger text-meta font-sans">
            {error}
          </div>
        )}

        <section className="border-t border-[var(--canvas-muted)] pt-10">
          <div className="flex items-baseline justify-between mb-8">
            <h2 className="font-serif text-h3 text-[var(--ink-charcoal)]">
              {t('pastMeetings.results') || 'Sonuçlar'}
            </h2>
            <span className="text-meta text-text-secondary font-sans">
              {displayedMeetings.length} {t('pastMeetings.records') || 'kayıt'}
            </span>
          </div>

          {loading ? (
            <div className="text-center py-16 text-text-secondary text-meta font-sans">
              {t('common.loading')}
            </div>
          ) : displayedMeetings.length === 0 ? (
            <div className="text-center py-16">
              <div className="w-14 h-14 flex items-center justify-center mx-auto mb-5 border border-[var(--canvas-muted)]">
                <Calendar className="w-6 h-6 text-[var(--accent-ochre-ink)]" strokeWidth={1.5} />
              </div>
              <p className="text-body text-text-secondary font-sans">{t('pastMeetings.noMeetings')}</p>
            </div>
          ) : (
            <ul className="border-t border-[var(--canvas-muted)]">
              {displayedMeetings.map((meeting) => (
                <li
                  key={meeting.id}
                  onClick={() => navigate(`/meetings/${meeting.id}`)}
                  className="group cursor-pointer px-1 py-8 border-b border-[var(--canvas-muted)] transition-colors hover:bg-[var(--canvas-muted)]/30"
                >
                  <div className="flex items-start justify-between gap-6">
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-3 mb-2 flex-wrap">
                        <h3 className="font-serif text-h3 text-[var(--ink-charcoal)]">
                          {meeting.title}
                        </h3>
                        <span className="text-micro uppercase tracking-[0.14em] text-text-secondary font-sans">
                          {t('meeting.past')}
                        </span>
                      </div>
                      <div className="flex items-center gap-2 text-meta text-text-secondary font-sans">
                        <Clock className="w-4 h-4" strokeWidth={1.5} />
                        <span>
                          {formatDate(meeting.start_time)} · {formatTime(meeting.start_time)} – {formatTime(meeting.end_time)}
                        </span>
                      </div>

                      {meeting.summary && (
                        <div className="mt-3 flex items-start gap-2 text-meta">
                          <FileText className="w-4 h-4 text-[var(--accent-ochre-ink)] flex-shrink-0 mt-0.5" strokeWidth={1.5} />
                          <p className="text-text-secondary font-sans line-clamp-2 leading-[1.55]">
                            {meeting.summary}
                          </p>
                        </div>
                      )}
                    </div>
                    <div className="flex items-center flex-wrap gap-2 shrink-0">
                      {(meeting.bot_status === 'joined' || meeting.bot_status === 'completed') && (
                        <span className="badge-primary">
                          {t('pastMeetings.botJoined') || 'Bot Katıldı'}
                        </span>
                      )}
                      {meeting.bot_status === 'failed' && (
                        <span className="inline-flex items-center px-3 py-1 rounded-full text-micro font-sans border border-danger/40 text-danger">
                          Bot Katılamadı
                        </span>
                      )}
                      {meeting.summary && (
                        <span className="badge">
                          {t('pastMeetings.hasSummary') || 'Özet Var'}
                        </span>
                      )}
                      {meeting.meeting_url && (
                        <a
                          href={meeting.meeting_url}
                          target="_blank"
                          rel="noopener noreferrer"
                          onClick={(e) => e.stopPropagation()}
                          className="inline-flex items-center gap-2 h-9 px-3 rounded-full border border-[var(--canvas-muted)] text-micro font-sans text-[var(--ink-charcoal)] hover:border-[var(--ink-secondary)] transition-colors"
                        >
                          <ExternalLink className="w-3.5 h-3.5" strokeWidth={1.5} />
                          Link
                        </a>
                      )}
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </div>
  )
}
