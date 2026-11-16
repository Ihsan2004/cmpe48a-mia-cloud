export default function DecisionViewHeader({
  meetingTitle,
  meetingDay,
  meetingTime,
  attendeeCount,
  chainColor,
  onBack,
  visible,
}) {
  if (!visible) return null

  return (
    <div className="decision-view-header">
      <button
        className="decision-view-header-back"
        onClick={onBack}
      >
        &larr; Back
      </button>

      <div className="decision-view-header-divider" />

      <div
        className="decision-view-header-dot"
        style={{ background: chainColor }}
      />

      <div className="decision-view-header-info">
        <div className="decision-view-header-title">
          {meetingTitle}
        </div>
        <div className="decision-view-header-meta">
          <span>{meetingDay} {meetingTime}</span>
          <span>{attendeeCount} attendee{attendeeCount !== 1 ? 's' : ''}</span>
        </div>
      </div>
    </div>
  )
}
