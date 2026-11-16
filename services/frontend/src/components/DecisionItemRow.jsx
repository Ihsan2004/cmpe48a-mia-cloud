import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Pencil, Trash2, CheckCheck, X, Ban } from 'lucide-react'

export default function DecisionItemRow({ item, onComplete, onDismiss, onDelete, onUpdate }) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  const [editText, setEditText] = useState(item.text)
  const [editOwner, setEditOwner] = useState(item.owner_name || '')
  const [loading, setLoading] = useState(false)

  const handleSaveEdit = async () => {
    setLoading(true)
    try {
      await onUpdate(item.id, editText, editOwner || null)
      setEditing(false)
    } finally {
      setLoading(false)
    }
  }

  const handleAction = async (action) => {
    setLoading(true)
    try {
      await action(item.id)
    } finally {
      setLoading(false)
    }
  }

  const statusColors = {
    confirmed: 'border-blue-500/30 bg-blue-500/5',
    completed: 'border-green-500/30 bg-green-500/5',
    dismissed: 'border-gray-500/30 bg-gray-500/5 opacity-60',
  }

  const statusLabels = {
    confirmed: 'Onaylı',
    completed: 'Tamamlandı',
    dismissed: 'Kapatıldı',
  }

  const typeLabels = {
    decision: '📋 Karar',
    action_item: '✅ Aksiyon',
    open_topic: '❓ Açık Konu',
  }

  if (editing) {
    return (
      <div className={`p-4 rounded-lg border ${statusColors[item.status] || 'border-surface-border'}`}>
        <div className="space-y-3">
          <textarea
            value={editText}
            onChange={(e) => setEditText(e.target.value)}
            className="w-full bg-surface border border-surface-border rounded-md p-3 text-sm text-text resize-none"
            rows={2}
          />
          <input
            type="text"
            value={editOwner}
            onChange={(e) => setEditOwner(e.target.value)}
            placeholder="Sahip (opsiyonel)"
            className="w-full bg-surface border border-surface-border rounded-md p-2 text-sm text-text"
          />
          <div className="flex gap-2">
            <button
              onClick={handleSaveEdit}
              disabled={loading || !editText.trim()}
              className="px-3 py-1.5 text-xs bg-primary text-white rounded-md hover:bg-primary/90 disabled:opacity-50"
            >
              {loading ? 'Kaydediliyor...' : 'Kaydet'}
            </button>
            <button
              onClick={() => { setEditing(false); setEditText(item.text); setEditOwner(item.owner_name || '') }}
              className="px-3 py-1.5 text-xs text-text-secondary border border-surface-border rounded-md hover:bg-surface"
            >
              İptal
            </button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className={`p-4 rounded-lg border ${statusColors[item.status] || 'border-surface-border'} transition-all`}>
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <span className="text-xs font-medium text-text-tertiary">
              {typeLabels[item.item_type] || item.item_type}
            </span>
            <span className={`text-xs px-1.5 py-0.5 rounded ${
              item.status === 'confirmed' ? 'bg-blue-500/20 text-blue-400' :
              item.status === 'completed' ? 'bg-green-500/20 text-[var(--accent-ochre-ink)]' :
              'bg-gray-500/20 text-gray-400'
            }`}>
              {statusLabels[item.status]}
            </span>
          </div>

          <p className="text-sm text-text mb-1">{item.text}</p>

          {item.owner_name && (
            <p className="text-xs text-text-tertiary">Sahip: {item.owner_name}</p>
          )}

          {item.source_quote && (
            <p className="text-xs text-text-tertiary mt-1 italic border-l-2 border-surface-border pl-2">
              "{item.source_quote}"
            </p>
          )}

          {item.context && (
            <p className="text-xs text-text-secondary mt-1.5">
              <span className="text-text-tertiary">Sebep:</span> {item.context}
            </p>
          )}

          {item.alternatives_considered && (
            <p className="text-xs text-text-secondary mt-1">
              <span className="text-text-tertiary">Alternatifler:</span> {item.alternatives_considered}
            </p>
          )}
        </div>

        <div className="flex items-center gap-1 shrink-0">
          {item.status === 'confirmed' && (
            <>
              <button
                onClick={() => handleAction(onComplete)}
                disabled={loading}
                className="p-1.5 text-[var(--accent-ochre-ink)] hover:bg-green-500/10 rounded-md transition-colors"
                title="Tamamla"
              >
                <CheckCheck className="w-4 h-4" />
              </button>
              <button
                onClick={() => handleAction(onDismiss)}
                disabled={loading}
                className="p-1.5 text-gray-400 hover:bg-gray-500/10 rounded-md transition-colors"
                title="Kapat"
              >
                <Ban className="w-4 h-4" />
              </button>
              <button
                onClick={() => setEditing(true)}
                disabled={loading}
                className="p-1.5 text-text-secondary hover:bg-surface rounded-md transition-colors"
                title="Düzenle"
              >
                <Pencil className="w-4 h-4" />
              </button>
              <button
                onClick={() => handleAction(onDelete)}
                disabled={loading}
                className="p-1.5 text-danger hover:bg-red-500/10 rounded-md transition-colors"
                title="Sil"
              >
                <Trash2 className="w-4 h-4" />
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
