// flowTransformer.js
// Transforms backend GET /api/flows/data response into xyflow-compatible
// node/edge structures matching the prototype's MeetingData, ChainEdgeData,
// and DecisionData interfaces.

// Chain color palette — Syncrun editorial tones keyed to the ochre accent family
// Restrained, warm, and all sit comfortably on the bone canvas.
const CHAIN_COLORS = [
  '#8b6b2e',  // accent-ochre-ink (primary accent)
  '#706e69',  // ink-secondary
  '#c9a054',  // accent-ochre (decorative fill)
  '#2d2b27',  // ink-charcoal
  '#a5824a',  // warm umber
  '#5d5a54',  // deep stone
  '#b28848',  // soft amber
  '#8a7e6c',  // driftwood
  '#4a4842',  // charcoal-soft
  '#d1b780',  // pale ochre
]

// Map JS Date.getDay() (0=Sun) to DayOfWeek string
const DAY_MAP = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat']

// Layout constants (matching prototype utils/layout.ts and utils/timeScale.ts)
const PIXELS_PER_HOUR = 100
const DAY_START_HOUR = 8
const DAY_START_MINUTES = DAY_START_HOUR * 60 // 480
const DAY_END_HOUR = 19
const TOTAL_HOURS = DAY_END_HOUR - DAY_START_HOUR // 11
const TIME_AXIS_HEIGHT = TOTAL_HOURS * PIXELS_PER_HOUR // 1100
const MIN_MEETING_HEIGHT = 30

const DAY_COLUMN_WIDTH = 260
const DAY_COLUMN_GAP = 12
const DAY_COLUMN_HEADER_HEIGHT = 44
const DAY_COLUMN_HEIGHT = DAY_COLUMN_HEADER_HEIGHT + TIME_AXIS_HEIGHT + 20
const MEETING_NODE_WIDTH = 236
const GHOST_GUTTER_WIDTH = 120

const DAY_LABELS = {
  mon: 'Pazartesi',
  tue: 'Sali',
  wed: 'Carsamba',
  thu: 'Persembe',
  fri: 'Cuma',
}

const ALL_DAYS = ['mon', 'tue', 'wed', 'thu', 'fri']

function timeToY(minutes) {
  return ((minutes - DAY_START_MINUTES) / 60) * PIXELS_PER_HOUR
}

function durationToHeight(startMinutes, endMinutes) {
  const raw = ((endMinutes - startMinutes) / 60) * PIXELS_PER_HOUR
  return Math.max(raw, MIN_MEETING_HEIGHT)
}

function getChainColor(seriesId, allSeriesIds) {
  const index = allSeriesIds.indexOf(seriesId)
  return CHAIN_COLORS[index % CHAIN_COLORS.length]
}

function dayColumnId(day) {
  return `day-${day}`
}

function meetingNodeId(meetingId, day) {
  return `meeting-${meetingId}-${day}`
}

function getTodayIndex() {
  const jsDay = new Date().getDay() // 0=Sun, 1=Mon, ..., 6=Sat
  if (jsDay === 0) return 4 // Sunday -> Fri
  if (jsDay === 6) return 4 // Saturday -> Fri
  return jsDay - 1
}

function get3DayWindow() {
  const todayIdx = getTodayIndex()
  const todayDay = ALL_DAYS[todayIdx]

  let indices
  if (todayIdx === 0) {
    indices = [4, 0, 1]
  } else if (todayIdx === 4) {
    indices = [2, 3, 4]
  } else {
    indices = [todayIdx - 1, todayIdx, todayIdx + 1]
  }

  const relativeLabels = (idx, pos) => {
    const base = DAY_LABELS[ALL_DAYS[idx]]
    if (pos === 0) return `${base} (Dun)`
    if (pos === 1) return `${base} (Bugun)`
    if (pos === 2) return `${base} (Yarin)`
    return base
  }

  return {
    days: indices.map(i => ALL_DAYS[i]),
    labels: indices.map((idx, pos) => relativeLabels(idx, pos)),
    todayDay,
  }
}

function get5DayWindow() {
  const todayIdx = getTodayIndex()
  return {
    days: [...ALL_DAYS],
    labels: ALL_DAYS.map(d => DAY_LABELS[d]),
    todayDay: ALL_DAYS[todayIdx],
  }
}

/**
 * Compute barycenter ordering for chain meetings to minimize edge crossings.
 * Assigns a slot index to each chain meeting node per day column.
 */
function computeBarycenterOrder(chainMeetings, dayOrder) {
  const slotMap = new Map()

  // Group by day
  const byDay = new Map()
  for (const m of chainMeetings) {
    const list = byDay.get(m.day) || []
    list.push(m)
    byDay.set(m.day, list)
  }

  // Previous day's resolved Y positions
  const prevPositions = new Map()

  for (const day of dayOrder) {
    const meetings = byDay.get(day)
    if (!meetings || meetings.length === 0) continue

    const withBarycenter = meetings.map((m) => {
      const neighbors = chainMeetings.filter(
        (other) => other.seriesId === m.seriesId && other.dayIndex < m.dayIndex,
      )

      if (neighbors.length === 0) {
        return { ...m, barycenter: m.timeY }
      }

      const sum = neighbors.reduce((acc, n) => {
        return acc + (prevPositions.get(n.nodeId) || n.timeY)
      }, 0)
      return { ...m, barycenter: sum / neighbors.length }
    })

    withBarycenter.sort((a, b) => a.barycenter - b.barycenter)

    withBarycenter.forEach((m, i) => {
      slotMap.set(m.nodeId, i)
      prevPositions.set(m.nodeId, m.timeY)
    })
  }

  return slotMap
}

/**
 * Transform API response { meetings, chains, decisions } into xyflow
 * nodes and edges ready for ReactFlow rendering.
 *
 * @param {object} apiData - Response from GET /api/flows/data
 * @param {string} viewMode - '3day' or '5day' (default: '3day')
 * @returns {{ nodes: Array, edges: Array, todayDay: string, chainLegend: Array, decisionsByMeeting: object }}
 */
export function transformFlowData(apiData, viewMode = '3day') {
  const { meetings = [], chains = [], decisions = [] } = apiData

  // Build chain lookup: chainId -> chain info
  const chainMap = {}
  chains.forEach(chain => {
    chainMap[chain.id] = chain
  })

  // Build decisions grouped by meeting: meetingId -> DecisionData[]
  const decisionsByMeeting = {}
  decisions.forEach(d => {
    if (!decisionsByMeeting[d.meeting_id]) {
      decisionsByMeeting[d.meeting_id] = []
    }
    let status = 'pending'
    if (d.status === 'confirmed') status = 'decided'
    else if (d.status === 'dismissed') status = 'revisit'

    decisionsByMeeting[d.meeting_id].push({
      id: d.id,
      title: d.text.substring(0, 60),
      description: d.text,
      status,
      linkedMeetingIds: [d.meeting_id],
      seriesId: d.chain_id || d.meeting_id,
    })
  })

  // Convert meetings to MeetingDef-like objects with day info
  const meetingDefs = meetings.map(m => {
    const startDate = new Date(m.start_time)
    const endDate = new Date(m.end_time)
    const dayIndex = startDate.getDay() // 0=Sun
    const day = DAY_MAP[dayIndex]
    const startMinutes = startDate.getHours() * 60 + startDate.getMinutes()
    const endMinutes = endDate.getHours() * 60 + endDate.getMinutes()

    return {
      id: m.id,
      title: m.title,
      startMinutes,
      endMinutes,
      day,
      attendees: (m.attendees || []).map(a => a.display_name || a.email),
      isRecurring: m.chain_id != null,
      seriesId: m.chain_id || undefined,
      description: m.description || '',
      decisionCount: m.decision_count || 0,
    }
  })

  // Collect all distinct seriesIds for color assignment
  const allSeriesIds = [...new Set(meetingDefs.filter(m => m.seriesId).map(m => m.seriesId))]

  // Get visible day window
  const windowData = viewMode === '3day' ? get3DayWindow() : get5DayWindow()
  const { days: visibleDays, labels, todayDay } = windowData
  const visibleDaysSet = new Set(visibleDays)

  const nodes = []
  const edges = []

  // 1. Day column nodes
  const todayScale = viewMode === '3day' ? 1.3 : 1.15
  let xOffset = GHOST_GUTTER_WIDTH + DAY_COLUMN_GAP
  const dayPositions = new Map()

  visibleDays.forEach((day, i) => {
    const isToday = day === todayDay
    const scale = isToday ? todayScale : 1
    const colWidth = Math.round(DAY_COLUMN_WIDTH * scale)

    dayPositions.set(day, { x: xOffset, width: colWidth })

    nodes.push({
      id: dayColumnId(day),
      type: 'dayColumn',
      position: { x: xOffset, y: 0 },
      data: { day, label: labels[i], isToday },
      draggable: false,
      selectable: false,
      style: { width: colWidth, height: DAY_COLUMN_HEIGHT },
    })

    xOffset += colWidth + DAY_COLUMN_GAP
  })

  // 2. Build chain meeting list for barycenter ordering (only visible days)
  const chainMeetings = []
  for (const m of meetingDefs) {
    if (!m.seriesId) continue
    if (!visibleDaysSet.has(m.day)) continue
    const nodeId = meetingNodeId(m.id, m.day)
    chainMeetings.push({
      nodeId,
      seriesId: m.seriesId,
      day: m.day,
      dayIndex: visibleDays.indexOf(m.day),
      timeY: timeToY(m.startMinutes),
    })
  }

  // 3. Compute barycenter order
  const slotMap = computeBarycenterOrder(chainMeetings, visibleDays)

  // 3b. Compute totalChainSlots per visible day
  const chainSlotsPerDay = new Map()
  for (const day of visibleDays) {
    const seriesInDay = new Set()
    for (const m of meetingDefs) {
      if (m.seriesId && m.day === day) {
        seriesInDay.add(m.seriesId)
      }
    }
    chainSlotsPerDay.set(day, seriesInDay.size)
  }

  // 4. Create meeting nodes
  const seriesMeetingIds = {}

  for (const m of meetingDefs) {
    if (!visibleDaysSet.has(m.day)) continue

    const nodeId = meetingNodeId(m.id, m.day)
    const dayPos = dayPositions.get(m.day)
    if (!dayPos) continue

    const nodeWidth = Math.min(MEETING_NODE_WIDTH, dayPos.width - 24)
    const chainColor = m.seriesId ? getChainColor(m.seriesId, allSeriesIds) : undefined

    nodes.push({
      id: nodeId,
      type: 'meeting',
      position: {
        x: (dayPos.width - nodeWidth) / 2,
        y: DAY_COLUMN_HEADER_HEIGHT + timeToY(m.startMinutes),
      },
      parentId: dayColumnId(m.day),
      extent: 'parent',
      data: {
        title: m.title,
        startMinutes: m.startMinutes,
        endMinutes: m.endMinutes,
        day: m.day,
        attendees: m.attendees,
        color: chainColor || '#94a3b8',
        isRecurring: m.isRecurring,
        seriesId: m.seriesId,
        description: m.description,
        chainSlot: slotMap.get(nodeId),
        totalChainSlots: m.seriesId ? chainSlotsPerDay.get(m.day) : undefined,
        chainColor,
        meetingId: m.id,
        decisionCount: m.decisionCount,
      },
      style: {
        width: nodeWidth,
        height: durationToHeight(m.startMinutes, m.endMinutes),
      },
    })

    if (m.seriesId) {
      if (!seriesMeetingIds[m.seriesId]) {
        seriesMeetingIds[m.seriesId] = []
      }
      seriesMeetingIds[m.seriesId].push({
        nodeId,
        dayIndex: visibleDays.indexOf(m.day),
        day: m.day,
      })
    }
  }

  // 5. Build series label lookup
  const seriesLabelMap = new Map()
  for (const m of meetingDefs) {
    if (m.seriesId && !seriesLabelMap.has(m.seriesId)) {
      seriesLabelMap.set(m.seriesId, m.title)
    }
  }

  // 6. Create chain edges between visible meetings
  for (const [seriesId, meetingList] of Object.entries(seriesMeetingIds)) {
    const sorted = [...meetingList].sort((a, b) => a.dayIndex - b.dayIndex)
    const chainColor = getChainColor(seriesId, allSeriesIds)
    const seriesLabel = seriesLabelMap.get(seriesId) || seriesId

    for (let i = 0; i < sorted.length - 1; i++) {
      edges.push({
        id: `edge-${sorted[i].nodeId}-${sorted[i + 1].nodeId}`,
        source: sorted[i].nodeId,
        target: sorted[i + 1].nodeId,
        sourceHandle: `chain-source-${seriesId}`,
        targetHandle: `chain-target-${seriesId}`,
        type: 'chain',
        data: { color: chainColor, seriesId, seriesLabel, showLabel: i === 0 },
      })
    }
  }

  // 7. Build chain legend entries
  const chainLegend = allSeriesIds.map(sid => ({
    seriesId: sid,
    name: seriesLabelMap.get(sid) || chainMap[sid]?.display_name || sid,
    color: getChainColor(sid, allSeriesIds),
  }))

  return { nodes, edges, todayDay, chainLegend, decisionsByMeeting }
}

// Re-export layout constants for components that need them
export {
  DAY_COLUMN_WIDTH,
  DAY_COLUMN_GAP,
  DAY_COLUMN_HEIGHT,
  DAY_COLUMN_HEADER_HEIGHT,
  MEETING_NODE_WIDTH,
  GHOST_GUTTER_WIDTH,
  PIXELS_PER_HOUR,
  DAY_START_HOUR,
  DAY_END_HOUR,
  TOTAL_HOURS,
  TIME_AXIS_HEIGHT,
  DAY_LABELS,
  ALL_DAYS,
  CHAIN_COLORS,
  timeToY,
  durationToHeight,
  getChainColor,
}
