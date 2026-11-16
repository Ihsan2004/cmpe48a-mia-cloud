import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Sparkles, Check, X, Loader, ExternalLink } from 'lucide-react'
import {
  listChainSuggestions,
  acceptChainSuggestion,
  declineChainSuggestion,
} from '../services/api'
import EmptyState from '../components/EmptyState'
import LoadingState from '../components/LoadingState'
import { showError, showSuccess } from '../utils/toast'

// ChainSuggestions is the inbox UI for Phase 3 of the chain-detection
// roadmap. The matcher's metadata-LLM fallback band (Phase 4) writes
// `mode='suggestion' status='pending'` rows to chain_decisions_v2;
// this page renders them and wires accept/decline through.
//
// The endpoint is shipped before suggestions actually exist (Phase 4
// hasn't cut over yet), so the empty state is the default. We render
// it as a friendly "you're all caught up" rather than an error so the
// nav entry never feels broken.
export default function ChainSuggestions() {
  const { t } = useTranslation()
  const [suggestions, setSuggestions] = useState([])
  const [loading, setLoading] = useState(true)
  const [actionInFlight, setActionInFlight] = useState(null) // decisionId currently being acted on

  useEffect(() => {
    load()
  }, [])

  const load = async () => {
    setLoading(true)
    try {
      const items = await listChainSuggestions(50)
      setSuggestions(items)
    } catch (err) {
      showError(t('chainSuggestions.loadError'))
    } finally {
      setLoading(false)
    }
  }

  const onAccept = async (id) => {
    setActionInFlight(id)
    try {
      await acceptChainSuggestion(id)
      // Optimistic remove — the row is no longer pending so it shouldn't
      // appear after refresh either. Saves an extra round-trip.
      setSuggestions((prev) => prev.filter((s) => s.DecisionID !== id))
      showSuccess(t('chainSuggestions.messages.accepted'))
    } catch (err) {
      showError(err.message || t('chainSuggestions.messages.actionFailed'))
    } finally {
      setActionInFlight(null)
    }
  }

  const onDecline = async (id) => {
    setActionInFlight(id)
    try {
      await declineChainSuggestion(id)
      setSuggestions((prev) => prev.filter((s) => s.DecisionID !== id))
      showSuccess(t('chainSuggestions.messages.declined'))
    } catch (err) {
      showError(err.message || t('chainSuggestions.messages.actionFailed'))
    } finally {
      setActionInFlight(null)
    }
  }

  if (loading) {
    return (
      <div className="max-w-4xl mx-auto px-6 py-10">
        <LoadingState message={t('chainSuggestions.loading')} />
      </div>
    )
  }

  return (
    <div className="max-w-4xl mx-auto px-6 py-10">
      <header className="mb-8">
        <h1 className="font-serif text-h2 text-[var(--ink-charcoal)] mb-2">
          {t('chainSuggestions.title')}
        </h1>
        <p className="text-meta text-text-secondary font-sans">
          {t('chainSuggestions.subtitle')}
        </p>
      </header>

      {suggestions.length === 0 ? (
        <EmptyState
          icon={Sparkles}
          title={t('chainSuggestions.empty')}
          description={t('chainSuggestions.subtitle')}
        />
      ) : (
        <ul className="divide-y divide-[var(--canvas-muted)]">
          {suggestions.map((s) => (
            <SuggestionRow
              key={s.DecisionID}
              suggestion={s}
              onAccept={onAccept}
              onDecline={onDecline}
              acting={actionInFlight === s.DecisionID}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

function SuggestionRow({ suggestion, onAccept, onDecline, acting }) {
  const { t } = useTranslation()
  const score = suggestion.WeightedScore != null ? Math.round(suggestion.WeightedScore * 100) : null
  const decisionCosine = suggestion.DecisionCosine != null ? Math.round(suggestion.DecisionCosine * 100) : null
  const partOverlap = suggestion.ParticipantOverlap != null ? Math.round(suggestion.ParticipantOverlap * 100) : null
  const titleSim = suggestion.TitleSimilarity != null ? Math.round(suggestion.TitleSimilarity * 100) : null

  return (
    <li className="py-5">
      <div className="flex items-start justify-between gap-6">
        <div className="flex-1 min-w-0">
          {/* Meeting → candidate chain summary line. The meeting title
              links to its detail page so the user can verify context
              without losing the inbox state. */}
          <div className="flex items-center gap-2 text-meta font-sans mb-2">
            <span className="text-text-tertiary uppercase tracking-wider text-micro">
              {t('chainSuggestions.newMeeting')}
            </span>
            {suggestion.MeetingID ? (
              <Link
                to={`/meetings/${suggestion.MeetingID}`}
                className="text-[var(--ink-charcoal)] hover:underline inline-flex items-center gap-1"
              >
                {suggestion.MeetingTitle || suggestion.MeetingID}
                <ExternalLink className="w-3 h-3" />
              </Link>
            ) : (
              <span className="text-[var(--ink-charcoal)]">{suggestion.MeetingTitle}</span>
            )}
          </div>

          <div className="flex items-center gap-2 text-meta font-sans mb-3">
            <span className="text-text-tertiary uppercase tracking-wider text-micro">
              {t('chainSuggestions.candidate')}
            </span>
            <span className="text-[var(--ink-charcoal)]">
              {suggestion.CandidateLabel}
            </span>
          </div>

          {/* Score breakdown — surfaced so the user can audit *why*
              the matcher proposed this. Hidden when all scores are
              null (defensive — shadow rows from outside the metadata
              band may not have all features populated). */}
          {(score != null || decisionCosine != null || partOverlap != null || titleSim != null) && (
            <div className="flex items-center gap-4 text-micro font-sans text-text-tertiary">
              {score != null && (
                <span>
                  {t('chainSuggestions.scores.weighted')}: <strong className="text-[var(--ink-charcoal)]">{score}%</strong>
                </span>
              )}
              {decisionCosine != null && (
                <span>
                  {t('chainSuggestions.scores.decision')}: <strong className="text-[var(--ink-charcoal)]">{decisionCosine}%</strong>
                </span>
              )}
              {partOverlap != null && (
                <span>
                  {t('chainSuggestions.scores.participants')}: <strong className="text-[var(--ink-charcoal)]">{partOverlap}%</strong>
                </span>
              )}
              {titleSim != null && (
                <span>
                  {t('chainSuggestions.scores.title')}: <strong className="text-[var(--ink-charcoal)]">{titleSim}%</strong>
                </span>
              )}
            </div>
          )}
        </div>

        <div className="flex items-center gap-2 shrink-0">
          <button
            type="button"
            onClick={() => onAccept(suggestion.DecisionID)}
            disabled={acting}
            className="inline-flex items-center gap-1.5 px-4 py-2 text-meta font-sans text-[var(--canvas-bone)] bg-[var(--ink-charcoal)] hover:opacity-90 transition-opacity disabled:opacity-50"
          >
            {acting ? <Loader className="w-3.5 h-3.5 animate-spin" /> : <Check className="w-3.5 h-3.5" />}
            {t('chainSuggestions.actions.accept')}
          </button>
          <button
            type="button"
            onClick={() => onDecline(suggestion.DecisionID)}
            disabled={acting}
            className="inline-flex items-center gap-1.5 px-4 py-2 text-meta font-sans text-text-secondary border border-[var(--canvas-muted)] hover:text-[var(--ink-charcoal)] transition-colors disabled:opacity-50"
          >
            <X className="w-3.5 h-3.5" />
            {t('chainSuggestions.actions.decline')}
          </button>
        </div>
      </div>
    </li>
  )
}
