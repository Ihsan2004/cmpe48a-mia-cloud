import { useState, useEffect } from 'react'
import { FileText, Save, Loader } from 'lucide-react'

export default function MeetingNoteEditor({ meetingId, initialContent, onSave }) {
  const [content, setContent] = useState(initialContent || '')
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    setContent(initialContent || '')
  }, [initialContent])

  const handleSave = async () => {
    setSaving(true)
    setSaved(false)
    try {
      await onSave(meetingId, content)
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="rounded-lg border border-surface-border bg-surface/50 p-4">
      <div className="flex items-center gap-2 mb-3">
        <FileText className="w-4 h-4 text-primary" />
        <h3 className="text-sm font-medium text-text">Kişisel Notlar</h3>
        <span className="text-xs text-text-tertiary">(Sadece siz görebilirsiniz)</span>
      </div>

      <textarea
        value={content}
        onChange={(e) => setContent(e.target.value)}
        placeholder="Toplantı hakkında kişisel notlarınızı buraya yazın..."
        className="w-full bg-dark-800 border border-surface-border rounded-md p-3 text-sm text-text resize-none min-h-[80px] focus:outline-none focus:border-primary/50"
        rows={3}
      />

      <div className="flex items-center justify-between mt-2">
        <span className="text-xs text-text-tertiary">
          {saved ? '✓ Kaydedildi' : ''}
        </span>
        <button
          onClick={handleSave}
          disabled={saving}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs bg-primary/10 text-primary border border-primary/20 rounded-md hover:bg-primary/20 transition-colors disabled:opacity-50"
        >
          {saving ? <Loader className="w-3 h-3 animate-spin" /> : <Save className="w-3 h-3" />}
          {saving ? 'Kaydediliyor...' : 'Kaydet'}
        </button>
      </div>
    </div>
  )
}
