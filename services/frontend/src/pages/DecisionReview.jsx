import { useState, useEffect } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, RefreshCw, Loader } from 'lucide-react'
import {
  getDecisionItems,
  updateDecisionItem,
  completeDecisionItem,
  dismissDecisionItem,
  deleteDecisionItem,
  retryExtraction,
  getMeetingDetail,
  getMeetingNote,
  saveMeetingNote,
  updateMeetingChain,
  retryAutoDetectChain,
} from '../services/api'
import DecisionItemRow from '../components/DecisionItemRow'
import MeetingNoteEditor from '../components/MeetingNoteEditor'
import ChainBadge from '../components/ChainBadge'
import ChainSelectorModal from '../components/ChainSelectorModal'
import LoadingState from '../components/LoadingState'
import ErrorBoundary from '../components/ErrorBoundary'
import { showError, showSuccess } from '../utils/toast'

export default function DecisionReview() {
  const { id: meetingId } = useParams()
  const { t } = useTranslation()

  const [meeting, setMeeting] = useState(null)
  const [decisions, setDecisions] = useState([])
  const [actionItems, setActionItems] = useState([])
  const [openTopics, setOpenTopics] = useState([])
  const [noteContent, setNoteContent] = useState('')
  const [loading, setLoading] = useState(true)
  const [extracting, setExtracting] = useState(false)
  const [error, setError] = useState(null)
  const [chainSelectorOpen, setChainSelectorOpen] = useState(false)

  const loadData = async () => {
    try {
      setLoading(true)
      setError(null)

      const [meetingData, itemsData, noteData] = await Promise.all([
        getMeetingDetail(meetingId).catch(() => null),
        getDecisionItems(meetingId).catch(() => ({ decisions: [], action_items: [], open_topics: [] })),
        getMeetingNote(meetingId).catch(() => ({ content: null })),
      ])

      if (meetingData) setMeeting(meetingData)
      setDecisions(itemsData.decisions || [])
      setActionItems(itemsData.action_items || [])
      setOpenTopics(itemsData.open_topics || [])
      setNoteContent(noteData.content || '')
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadData()
  }, [meetingId])

  const handleComplete = async (itemId) => {
    try {
      await completeDecisionItem(itemId)
      showSuccess('Tamamlandı')
      loadData()
    } catch (err) {
      showError(err.message)
    }
  }

  const handleDismiss = async (itemId) => {
    try {
      await dismissDecisionItem(itemId)
      showSuccess('Kapatıldı')
      loadData()
    } catch (err) {
      showError(err.message)
    }
  }

  const handleDelete = async (itemId) => {
    try {
      await deleteDecisionItem(itemId)
      showSuccess('Silindi')
      loadData()
    } catch (err) {
      showError(err.message)
    }
  }

  const handleUpdate = async (itemId, text, ownerName) => {
    try {
      await updateDecisionItem(itemId, text, ownerName)
      showSuccess('Güncellendi')
      loadData()
    } catch (err) {
      showError(err.message)
    }
  }

  const handleRetryExtraction = async () => {
    setExtracting(true)
    try {
      await retryExtraction(meetingId)
      showSuccess('Extraction başlatıldı, birkaç saniye sonra sayfayı yenileyin')
      setTimeout(loadData, 5000)
    } catch (err) {
      showError(err.message)
    } finally {
      setExtracting(false)
    }
  }

  const handleSaveNote = async (_, content) => {
    try {
      await saveMeetingNote(meetingId, content)
      showSuccess('Not kaydedildi')
    } catch (err) {
      showError(err.message)
    }
  }

  if (loading) return <LoadingState />

  const totalItems = decisions.length + actionItems.length + openTopics.length
  const hasNoItems = totalItems === 0

  return (
    <ErrorBoundary>
      <div className="max-w-4xl mx-auto p-6">
        {/* Header */}
        <div className="flex items-center gap-3 mb-6">
          <Link
            to={meeting ? `/meetings/${meetingId}` : '/dashboard'}
            className="p-2 text-text-secondary hover:text-text hover:bg-surface rounded-md transition-colors"
          >
            <ArrowLeft className="w-5 h-5" />
          </Link>
          <div className="flex-1">
            <h1 className="text-xl font-serif text-text">
              {meeting?.title || 'Toplantı Kararları'}
            </h1>
            {meeting?.start_time && (
              <p className="text-sm text-text-tertiary">
                {new Date(meeting.start_time).toLocaleDateString('tr-TR', {
                  day: 'numeric', month: 'long', year: 'numeric',
                  hour: '2-digit', minute: '2-digit',
                })}
              </p>
            )}
            {meeting && (
              <div className="mt-1">
                <ChainBadge
                  chainId={meeting.chain_id}
                  chainType={meeting.chain_type}
                  chainAssignmentMethod={meeting.chain_assignment_method}
                  meetingId={meetingId}
                  onMoveToChain={() => setChainSelectorOpen(true)}
                  onRemoveChain={async (id) => {
                    try {
                      await updateMeetingChain(id, null)
                      setMeeting(prev => ({
                        ...prev,
                        chain_id: null,
                        chain_type: null,
                        chain_assignment_method: 'manual_removed',
                      }))
                      showSuccess('Toplantı zincirden çıkarıldı')
                    } catch (err) {
                      showError(err.message)
                    }
                  }}
                  onRetryAutoDetect={async (id) => {
                    try {
                      await retryAutoDetectChain(id)
                      setMeeting(prev => ({
                        ...prev,
                        chain_assignment_method: null,
                        chain_assignment_locked_at: null,
                      }))
                      showSuccess('Otomatik tespit kilidi temizlendi')
                    } catch (err) {
                      showError(err.message)
                    }
                  }}
                />
                <ChainSelectorModal
                  open={chainSelectorOpen}
                  currentChainId={meeting.chain_id}
                  onClose={() => setChainSelectorOpen(false)}
                  onSelect={async (chainId) => {
                    try {
                      await updateMeetingChain(meetingId, chainId)
                      setMeeting(prev => ({
                        ...prev,
                        chain_id: chainId,
                        chain_assignment_method: 'manual_user',
                      }))
                      setChainSelectorOpen(false)
                      showSuccess(t('chain.messages.moved'))
                    } catch (err) {
                      showError(err.message)
                    }
                  }}
                />
              </div>
            )}
          </div>
          <button
            onClick={handleRetryExtraction}
            disabled={extracting}
            className="flex items-center gap-1.5 px-3 py-2 text-xs border border-surface-border text-text-secondary rounded-md hover:bg-surface transition-colors disabled:opacity-50"
          >
            {extracting ? <Loader className="w-3.5 h-3.5 animate-spin" /> : <RefreshCw className="w-3.5 h-3.5" />}
            {extracting ? 'Çıkarılıyor...' : 'Yeniden Çıkar'}
          </button>
        </div>

        {error && (
          <div className="mb-4 p-3 bg-red-500/10 border border-red-500/20 rounded-md text-sm text-danger">
            {error}
          </div>
        )}

        {/* Decision Items */}
        {hasNoItems ? (
          <div className="text-center py-12 text-text-tertiary">
            <p className="mb-2">Bu toplantı için henüz karar çıkarılmamış.</p>
            <button
              onClick={handleRetryExtraction}
              disabled={extracting}
              className="text-primary hover:underline text-sm"
            >
              Şimdi çıkar
            </button>
          </div>
        ) : (
          <div className="space-y-6">
            {/* Decisions */}
            {decisions.length > 0 && (
              <section>
                <h2 className="text-sm font-medium text-text-secondary mb-3 flex items-center gap-2">
                  📋 Kararlar
                  <span className="text-xs bg-surface px-1.5 py-0.5 rounded">{decisions.length}</span>
                </h2>
                <div className="space-y-2">
                  {decisions.map(item => (
                    <DecisionItemRow
                      key={item.id}
                      item={item}
                      onComplete={handleComplete}
                      onDismiss={handleDismiss}
                      onDelete={handleDelete}
                      onUpdate={handleUpdate}
                    />
                  ))}
                </div>
              </section>
            )}

            {/* Action Items */}
            {actionItems.length > 0 && (
              <section>
                <h2 className="text-sm font-medium text-text-secondary mb-3 flex items-center gap-2">
                  ✅ Aksiyonlar
                  <span className="text-xs bg-surface px-1.5 py-0.5 rounded">{actionItems.length}</span>
                </h2>
                <div className="space-y-2">
                  {actionItems.map(item => (
                    <DecisionItemRow
                      key={item.id}
                      item={item}
                      onComplete={handleComplete}
                      onDismiss={handleDismiss}
                      onDelete={handleDelete}
                      onUpdate={handleUpdate}
                    />
                  ))}
                </div>
              </section>
            )}

            {/* Open Topics */}
            {openTopics.length > 0 && (
              <section>
                <h2 className="text-sm font-medium text-text-secondary mb-3 flex items-center gap-2">
                  ❓ Açık Konular
                  <span className="text-xs bg-surface px-1.5 py-0.5 rounded">{openTopics.length}</span>
                </h2>
                <div className="space-y-2">
                  {openTopics.map(item => (
                    <DecisionItemRow
                      key={item.id}
                      item={item}
                      onComplete={handleComplete}
                      onDismiss={handleDismiss}
                      onDelete={handleDelete}
                      onUpdate={handleUpdate}
                    />
                  ))}
                </div>
              </section>
            )}
          </div>
        )}

        {/* Personal Notes */}
        <div className="mt-8">
          <MeetingNoteEditor
            meetingId={meetingId}
            initialContent={noteContent}
            onSave={handleSaveNote}
          />
        </div>
      </div>
    </ErrorBoundary>
  )
}
