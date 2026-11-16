import { useTranslation } from 'react-i18next'

export default function LoadingState({ message }) {
  const { t } = useTranslation()
  return (
    <div className="text-center py-12">
      <div className="w-8 h-8 border-2 border-[var(--canvas-muted)] border-t-[var(--accent-ochre-ink)] rounded-full animate-spin mx-auto mb-3" />
      <p className="text-text-secondary text-meta font-sans">{message || t('common.loading')}</p>
    </div>
  )
}
