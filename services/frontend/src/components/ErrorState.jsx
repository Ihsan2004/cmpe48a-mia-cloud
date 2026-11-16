import { AlertTriangle, RefreshCw } from 'lucide-react'

export default function ErrorState({ message, onRetry }) {
  return (
    <div className="text-center py-12">
      <div className="w-14 h-14 flex items-center justify-center mx-auto mb-4 border border-[var(--canvas-muted)]">
        <AlertTriangle className="w-6 h-6 text-danger" />
      </div>
      <p className="text-danger text-meta font-sans mb-4">{message || 'Bir hata olustu.'}</p>
      {onRetry && (
        <button onClick={onRetry} className="btn-secondary inline-flex items-center gap-2 text-meta mx-auto">
          <RefreshCw className="w-4 h-4" />
          Tekrar Dene
        </button>
      )}
    </div>
  )
}
