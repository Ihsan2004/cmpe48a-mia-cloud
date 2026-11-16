export default function DecisionContainerNode({ data }) {
  return (
    <div className="decision-container">
      <div className="decision-container-label" style={{ borderBottomColor: data.chainColor + '30' }}>
        <div className="decision-container-dot" style={{ background: data.chainColor }} />
        <span className="decision-container-title">{data.meetingTitle}</span>
        <span className="decision-container-day">{data.meetingDay}</span>
      </div>
    </div>
  )
}
