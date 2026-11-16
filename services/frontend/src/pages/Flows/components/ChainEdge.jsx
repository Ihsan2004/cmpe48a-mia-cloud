import { getBezierPath, BaseEdge, EdgeLabelRenderer } from '@xyflow/react'

export default function ChainEdge({
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
}) {
  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    curvature: 0.25,
  })

  const isHighlighted = data?.isHighlighted === true
  const isDimmed = data?.isDimmed === true

  return (
    <>
      <BaseEdge
        path={edgePath}
        interactionWidth={20}
        style={{
          stroke: data?.color || 'var(--mia-accent)',
          strokeWidth: isHighlighted ? 2.5 : 1.5,
          strokeDasharray: isHighlighted ? 'none' : '3 4',
          fill: 'none',
          opacity: isDimmed ? 0.15 : 0.65,
          transition: 'opacity 150ms ease, stroke-width 150ms ease, stroke-dasharray 150ms ease',
        }}
      />
      {isHighlighted && data?.showLabel && (
        <EdgeLabelRenderer>
          <div
            style={{
              position: 'absolute',
              transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
              pointerEvents: 'none',
              fontSize: '11px',
              fontWeight: 400,
              color: 'var(--mia-text)',
              background: 'var(--mia-bg)',
              border: '1px solid var(--mia-border)',
              padding: '2px 8px',
              borderRadius: '9999px',
              fontFamily: 'var(--mia-font-body)',
              textTransform: 'uppercase',
              letterSpacing: '0.14em',
              whiteSpace: 'nowrap',
            }}
          >
            {data.seriesLabel}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}
