import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { FileText, ThumbsUp, ThumbsDown, Clock, ChevronDown, ChevronUp } from 'lucide-react'
import { submitBriefFeedback, getBriefDetail } from '../services/api'
import { showSuccess, showError } from '../utils/toast'
import MarkdownRenderer from './MarkdownRenderer'

export default function BriefCard({ brief, meetingTitle, meetingTime }) {
  const navigate = useNavigate()
  const [expanded, setExpanded] = useState(false)
  const [feedbackSent, setFeedbackSent] = useState(false)
  const [feedbackLoading, setFeedbackLoading] = useState(false)
  const [opened, setOpened] = useState(!!brief.opened_at)

  const handleExpand = async () => {
    const willExpand = !expanded
    setExpanded(willExpand)
    if (willExpand && !opened) {
      setOpened(true)
      getBriefDetail(brief.target_meeting_id).catch(() => {})
    }
  }

  const handleFeedback = async (feedback) => {
    setFeedbackLoading(true)
    try {
      await submitBriefFeedback(brief.target_meeting_id, feedback, null)
      setFeedbackSent(true)
      showSuccess(feedback === 'useful' ? 'Teşekkürler!' : 'Geri bildiriminiz alındı')
    } catch (err) {
      showError(err.message)
    } finally {
      setFeedbackLoading(false)
    }
  }

  const previewContent = brief.content?.slice(0, 200) + (brief.content?.length > 200 ? '...' : '')

  return (
    <div className="card border-primary/20 hover:border-primary/40 transition-all">
      {/* Header */}
      <div className="flex items-start justify-between mb-3">
        <div className="flex items-center gap-2">
          <FileText className="w-4 h-4 text-primary" />
          <h3 className="text-sm font-serif text-text">{meetingTitle}</h3>
        </div>
        <div className="flex items-center gap-1 text-xs text-text-tertiary">
          <Clock className="w-3 h-3" />
          {meetingTime && new Date(meetingTime).toLocaleTimeString('tr-TR', { hour: '2-digit', minute: '2-digit' })}
        </div>
      </div>

      {/* Content */}
      <div className="text-sm text-text-secondary mb-3">
        {expanded ? (
          <div className="whitespace-pre-wrap">
            <MarkdownRenderer content={brief.content} />
          </div>
        ) : (
          <p>{previewContent}</p>
        )}
      </div>

      {/* Actions */}
      <div className="flex items-center justify-between">
        <button
          onClick={handleExpand}
          className="flex items-center gap-1 text-xs text-primary hover:underline"
        >
          {expanded ? <ChevronUp className="w-3 h-3" /> : <ChevronDown className="w-3 h-3" />}
          {expanded ? 'Kapat' : 'Devamını oku'}
        </button>

        <div className="flex items-center gap-2">
          {!feedbackSent ? (
            <>
              <button
                onClick={() => handleFeedback('useful')}
                disabled={feedbackLoading}
                className="flex items-center gap-1 px-2 py-1 text-xs text-[var(--accent-ochre-ink)] hover:bg-green-500/10 rounded transition-colors disabled:opacity-50"
              >
                <ThumbsUp className="w-3 h-3" />
                Faydalı
              </button>
              <button
                onClick={() => handleFeedback('not_useful')}
                disabled={feedbackLoading}
                className="flex items-center gap-1 px-2 py-1 text-xs text-danger hover:bg-red-500/10 rounded transition-colors disabled:opacity-50"
              >
                <ThumbsDown className="w-3 h-3" />
                Faydalı değil
              </button>
            </>
          ) : (
            <span className="text-xs text-text-tertiary">✓ Geri bildirim alındı</span>
          )}

          <button
            onClick={() => navigate(`/meetings/${brief.target_meeting_id}/review`)}
            className="text-xs text-primary hover:underline ml-2"
          >
            Kararları Gör →
          </button>
        </div>
      </div>

      {/* Generation method badge */}
      {brief.generation_method === 'template_fallback' && (
        <div className="mt-2 text-xs text-text-tertiary">
          ⚠️ Şablon ile oluşturuldu (AI brief hazırlanamadı)
        </div>
      )}
    </div>
  )
}
