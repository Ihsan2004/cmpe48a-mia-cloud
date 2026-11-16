// decisionViewData.js
// Builds xyflow nodes/edges for the decision drill-down view from real API data.
// Adapted from xyflow-test/src/data/decisionData.ts with mock data removed.

import { getChainColor, DAY_LABELS } from './flowTransformer'

// Layout constants for decision region
const DECISION_REGION_Y = 2000
const DECISION_NODE_WIDTH = 260
const DECISION_NODE_HEIGHT = 90
const DECISION_CONTAINER_PADDING = 40
const DECISION_NODE_GAP = 20
const CONTAINER_HEADER_HEIGHT = 40

/**
 * Format minutes to HH:MM - HH:MM time range string.
 * @param {number} startMinutes
 * @param {number} endMinutes
 * @returns {string}
 */
export function formatTimeRange(startMinutes, endMinutes) {
  const fmt = (m) => {
    const h = Math.floor(m / 60)
    const min = m % 60
    return `${h.toString().padStart(2, '0')}:${min.toString().padStart(2, '0')}`
  }
  return `${fmt(startMinutes)} - ${fmt(endMinutes)}`
}

/**
 * Build xyflow nodes and edges for decision drill-down view.
 *
 * @param {string} meetingNodeId - xyflow node ID of the clicked meeting
 * @param {string} seriesId - chain ID for color lookup
 * @param {Array} decisions - from decisionsByMeeting[rawMeetingId]
 * @param {object} meetingInfo - { title, day, startMinutes, endMinutes, attendees }
 * @param {Array} allSeriesIds - for chain color lookup
 * @param {number} anchorX - column X position for centering
 * @returns {{ nodes: Array, edges: Array, bounds: { x: number, y: number, width: number, height: number } }}
 */
export function buildDecisionViewData(meetingNodeId, seriesId, decisions, meetingInfo, allSeriesIds, anchorX) {
  const baseX = anchorX ?? 100
  const nodes = []
  const edges = []

  if (!meetingInfo) {
    return { nodes: [], edges: [], bounds: { x: baseX, y: DECISION_REGION_Y, width: 400, height: 300 } }
  }

  if (!decisions || decisions.length === 0) {
    return { nodes: [], edges: [], bounds: { x: baseX, y: DECISION_REGION_Y, width: 400, height: 300 } }
  }

  const chainColor = getChainColor(seriesId, allSeriesIds)

  // Build rows: each decision gets its own row (no cross-meeting links in v1)
  const rows = decisions.map(dec => [dec])

  // Container dimensions
  const containerX = baseX
  const containerY = DECISION_REGION_Y
  const maxCols = 1  // single-column since no pairs
  const containerWidth = DECISION_CONTAINER_PADDING * 2 +
    maxCols * DECISION_NODE_WIDTH + (maxCols - 1) * DECISION_NODE_GAP
  const containerHeight = CONTAINER_HEADER_HEIGHT + DECISION_CONTAINER_PADDING * 2 +
    rows.length * DECISION_NODE_HEIGHT + (rows.length - 1) * DECISION_NODE_GAP

  // Container node
  nodes.push({
    id: `decision-container-${seriesId}`,
    type: 'decisionContainer',
    position: { x: containerX, y: containerY },
    data: {
      chainColor,
      meetingTitle: meetingInfo.title,
      meetingDay: DAY_LABELS[meetingInfo.day] || meetingInfo.day,
    },
    draggable: false,
    selectable: false,
    zIndex: -1,
    style: { width: containerWidth, height: containerHeight },
  })

  // Decision nodes
  const innerStartY = containerY + CONTAINER_HEADER_HEIGHT + DECISION_CONTAINER_PADDING

  for (let rowIdx = 0; rowIdx < rows.length; rowIdx++) {
    const row = rows[rowIdx]
    const y = innerStartY + rowIdx * (DECISION_NODE_HEIGHT + DECISION_NODE_GAP)

    for (let colIdx = 0; colIdx < row.length; colIdx++) {
      const decision = row[colIdx]
      const nodeId = `decision-node-${decision.id}`
      const x = containerX + DECISION_CONTAINER_PADDING + colIdx * (DECISION_NODE_WIDTH + DECISION_NODE_GAP)

      nodes.push({
        id: nodeId,
        type: 'decision',
        position: { x, y },
        data: { decision, chainColor, sharedWith: [] },
        draggable: false,
        selectable: false,
        style: { width: DECISION_NODE_WIDTH, height: DECISION_NODE_HEIGHT },
      })
    }
  }

  // Bounding rect
  const padding = 80
  let minX = containerX
  let minY = containerY
  let maxX = containerX + containerWidth
  let maxY = containerY + containerHeight

  for (const node of nodes) {
    const nx = node.position.x
    const ny = node.position.y
    const nw = node.style?.width ?? 0
    const nh = node.style?.height ?? 0
    maxX = Math.max(maxX, nx + nw)
    maxY = Math.max(maxY, ny + nh)
    minX = Math.min(minX, nx)
    minY = Math.min(minY, ny)
  }

  return {
    nodes,
    edges,
    bounds: { x: minX - padding, y: minY - padding, width: maxX - minX + padding * 2, height: maxY - minY + padding * 2 },
  }
}
