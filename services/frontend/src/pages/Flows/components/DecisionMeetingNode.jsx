import { Handle, Position } from '@xyflow/react'
import { DAY_LABELS } from '../../../utils/flowTransformer'

export default function DecisionMeetingNode({ data }) {
  const { title, day, chainColor, isFocusedMeeting } = data
  const className = `decision-meeting-node ${isFocusedMeeting ? 'decision-meeting-node--focused' : 'decision-meeting-node--external'}`

  return (
    <div className={className}>
      <div
        className="decision-meeting-color-bar"
        style={{ background: chainColor }}
      />
      <div className="decision-meeting-title">{title}</div>
      <div className="decision-meeting-day">{DAY_LABELS[day]}</div>
      <Handle
        type="source"
        position={Position.Right}
        id="meeting-source"
        style={{
          background: chainColor,
          width: 6,
          height: 6,
          border: 'none',
        }}
      />
    </div>
  )
}
