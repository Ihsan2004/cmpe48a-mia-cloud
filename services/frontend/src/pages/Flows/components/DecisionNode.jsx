import { Handle, Position } from '@xyflow/react'

const STATUS_COLORS = {
  decided: 'var(--accent-ochre-ink)',
  pending: 'var(--ink-charcoal)',
  revisit: 'var(--ink-secondary)',
}

export default function DecisionNode({ data }) {
  const { decision, chainColor, sharedWith, isExternal } = data
  const statusColor = STATUS_COLORS[decision.status] ?? 'var(--ink-secondary)'
  const className = `decision-node${isExternal ? ' decision-node--external' : ''}`

  return (
    <div className={className}>
      <div
        className="decision-node-color-bar"
        style={{ background: chainColor }}
      />
      <div className="decision-node-header">
        <div className="decision-node-title">{decision.title}</div>
        <span
          className="decision-node-status"
          style={{ color: statusColor }}
        >
          {decision.status}
        </span>
      </div>
      {!isExternal && (
        <div className="decision-node-description">{decision.description}</div>
      )}
      {sharedWith && sharedWith.length > 0 && (
        <div className="decision-node-shared">
          Also in: {sharedWith.join(', ')}
        </div>
      )}
      <Handle
        type="target"
        position={Position.Left}
        id="decision-target-left"
        style={{ background: chainColor, width: 6, height: 6, border: 'none' }}
      />
      <Handle
        type="target"
        position={Position.Right}
        id="decision-target-right"
        style={{ background: chainColor, width: 6, height: 6, border: 'none' }}
      />
      <Handle
        type="source"
        position={Position.Left}
        id="decision-source-left"
        style={{ background: chainColor, width: 6, height: 6, border: 'none' }}
      />
      <Handle
        type="source"
        position={Position.Right}
        id="decision-source-right"
        style={{ background: chainColor, width: 6, height: 6, border: 'none' }}
      />
    </div>
  )
}
