export default function DayColumnNode({ data }) {
  const isToday = data.isToday === true

  return (
    <div
      className={`day-column-node ${isToday ? 'day-column-node--today' : ''}`}
      style={{ width: '100%', height: '100%' }}
    >
      <div className="day-column-header">
        <span
          className="nodrag"
          style={{
            fontWeight: 400,
            textTransform: 'uppercase',
            letterSpacing: '0.14em',
            fontSize: '13px',
            fontFamily: 'var(--mia-font-body)',
            color: isToday ? 'var(--mia-primary)' : 'var(--mia-text-secondary)',
          }}
        >
          {data.label}
        </span>
      </div>
      <div className="day-column-body" />
    </div>
  )
}
