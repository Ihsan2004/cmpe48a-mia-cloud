import { Handle, Position } from '@xyflow/react'
import { useNavigate } from 'react-router-dom'

function formatTimeRange(startMinutes, endMinutes) {
  const fmt = (m) => {
    const h = Math.floor(m / 60)
    const min = m % 60
    return `${h.toString().padStart(2, '0')}:${min.toString().padStart(2, '0')}`
  }
  return `${fmt(startMinutes)} - ${fmt(endMinutes)}`
}

function chainHandleTop(chainSlot, totalSlots) {
  if (totalSlots <= 1) return '50%'
  const padding = 20
  const range = 100 - 2 * padding
  return `${padding + (chainSlot / (totalSlots - 1)) * range}%`
}

export default function MeetingNode({ data, selected }) {
  const hasChain = data.seriesId != null && data.chainSlot != null
  const isDimmed = data.isDimmed === true
  const isGhost = data.isGhost === true
  const navigate = useNavigate()

  const handleDecisionClick = (e) => {
    e.stopPropagation()
    if (data.meetingId) {
      navigate(`/meetings/${data.meetingId}/review`)
    }
  }

  return (
    <div
      className={`meeting-node ${selected ? 'meeting-node--selected' : ''} ${isGhost ? 'meeting-node--ghost' : ''}`}
      style={{
        height: '100%',
        overflow: 'hidden',
        opacity: isDimmed ? 0.2 : isGhost ? 0.6 : 1,
        transition: 'opacity 150ms ease',
      }}
    >
      {data.chainColor && !isGhost && (
        <div style={{
          height: '3px',
          background: data.chainColor,
          borderRadius: '4px 4px 0 0',
          marginBottom: '6px',
          marginTop: '-8px',
          marginLeft: '-10px',
          marginRight: '-10px',
        }} />
      )}
      <div className="meeting-node-content">
        {isGhost && data.ghostDate && (
          <div className="meeting-node-ghost-date">{data.ghostDate}</div>
        )}
        <div className="meeting-node-title" style={{ fontSize: isGhost ? '11px' : undefined }}>
          {data.title}
        </div>
        {!isGhost && (
          <>
            <div className="meeting-node-time">{formatTimeRange(data.startMinutes, data.endMinutes)}</div>
            <div className="meeting-node-attendees">
              {data.attendees.slice(0, 3).map((a) => (
                <span key={a} className="meeting-avatar" style={{ backgroundColor: data.color }}>
                  {a[0]}
                </span>
              ))}
              {data.attendees.length > 3 && (
                <span className="meeting-avatar meeting-avatar--more">
                  +{data.attendees.length - 3}
                </span>
              )}
            </div>
            {data.isRecurring && <span className="meeting-recurring-badge">&#x21bb;</span>}
            {data.decisionCount > 0 && (
              <span className="meeting-decision-badge" onClick={handleDecisionClick} title="Kararlar">
                {data.decisionCount}
              </span>
            )}
          </>
        )}
      </div>
      {hasChain && (
        <>
          <Handle
            type="target"
            position={Position.Left}
            id={`chain-target-${data.seriesId}`}
            style={{
              top: chainHandleTop(data.chainSlot, data.totalChainSlots || 1),
              background: data.chainColor || 'var(--mia-accent)',
              width: 6,
              height: 6,
              border: 'none',
            }}
          />
          <Handle
            type="source"
            position={Position.Right}
            id={`chain-source-${data.seriesId}`}
            style={{
              top: chainHandleTop(data.chainSlot, data.totalChainSlots || 1),
              background: data.chainColor || 'var(--mia-accent)',
              width: 6,
              height: 6,
              border: 'none',
            }}
          />
        </>
      )}
    </div>
  )
}
