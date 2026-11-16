import { getBezierPath, BaseEdge } from '@xyflow/react'

export default function DecisionEdge({
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
}) {
  const [edgePath] = getBezierPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    curvature: 0.25,
  })

  return (
    <BaseEdge
      path={edgePath}
      style={{
        stroke: data?.color ?? 'var(--mia-accent)',
        strokeWidth: 1.5,
        strokeDasharray: '3 4',
        fill: 'none',
        opacity: data?.isExternal ? 0.35 : 0.65,
        transition: 'opacity 150ms ease',
      }}
    />
  )
}
