import { useState, useMemo, useCallback, useRef, useEffect, useReducer } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  BackgroundVariant,
  useReactFlow,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import DayColumnNode from './components/DayColumnNode'
import MeetingNode from './components/MeetingNode'
import ChainEdge from './components/ChainEdge'
import DecisionNode from './components/DecisionNode'
import DecisionContainerNode from './components/DecisionContainerNode'
import DecisionMeetingNode from './components/DecisionMeetingNode'
import DecisionEdge from './components/DecisionEdge'
import DecisionViewHeader from './components/DecisionViewHeader'
import { getFlowData } from '../../services/api'
import { transformFlowData } from '../../utils/flowTransformer'
import { buildDecisionViewData } from '../../utils/decisionViewData'
import LoadingState from '../../components/LoadingState'
import ErrorState from '../../components/ErrorState'
import EmptyState from '../../components/EmptyState'

import './flows.css'

const nodeTypes = {
  dayColumn: DayColumnNode,
  meeting: MeetingNode,
  decision: DecisionNode,
  decisionContainer: DecisionContainerNode,
  decisionMeeting: DecisionMeetingNode,
}

const edgeTypes = {
  chain: ChainEdge,
  decisionEdge: DecisionEdge,
}

const FIT_VIEW_PADDING = 0.08
const VIEWPORT_ANIMATION_DURATION = 500
const DECISION_FIT_VIEW_PADDING = 0.08

function viewStateReducer(state, action) {
  switch (action.type) {
    case 'CLICK_MEETING':
      if (state.type !== 'weekly') return state
      return { type: 'transitioning_out', meetingId: action.meetingId, seriesId: action.seriesId }
    case 'TRANSITION_OUT_COMPLETE':
      if (state.type !== 'transitioning_out') return state
      return { type: 'decision', meetingId: state.meetingId, seriesId: state.seriesId }
    case 'GO_BACK':
      if (state.type !== 'decision') return state
      return { type: 'transitioning_in', meetingId: state.meetingId, seriesId: state.seriesId }
    case 'TRANSITION_IN_COMPLETE':
      if (state.type !== 'transitioning_in') return state
      return { type: 'weekly' }
    default:
      return state
  }
}

function MeetingFlowInner() {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [flowData, setFlowData] = useState(null)
  const [viewMode, setViewMode] = useState('3day')
  const [scope, setScope] = useState('my')
  const [highlightedChainId, setHighlightedChainId] = useState(null)
  const [viewState, dispatch] = useReducer(viewStateReducer, { type: 'weekly' })
  const leaveTimerRef = useRef(null)
  const reactFlow = useReactFlow()
  const navigate = useNavigate()
  const { t } = useTranslation()

  // Fetch data from API (re-fetch when scope changes)
  useEffect(() => {
    async function fetchData() {
      try {
        setLoading(true)
        setError(null)
        const apiData = await getFlowData(undefined, undefined, scope)
        setFlowData(apiData)
      } catch (err) {
        setError(err.message || t('flows.loadError'))
      } finally {
        setLoading(false)
      }
    }
    fetchData()
  }, [scope, t])

  // Reset to weekly view when scope changes
  useEffect(() => {
    if (viewState.type !== 'weekly') {
      dispatch({ type: 'TRANSITION_IN_COMPLETE' }) // force back to weekly
    }
  }, [scope]) // eslint-disable-line react-hooks/exhaustive-deps

  // Transform API data to xyflow nodes/edges
  const { nodes: initNodes, edges: initEdges, chainLegend, decisionsByMeeting } = useMemo(() => {
    if (!flowData) return { nodes: [], edges: [], chainLegend: [], decisionsByMeeting: {} }
    return transformFlowData(flowData, viewMode)
  }, [flowData, viewMode])

  // Build decision view data when in decision-related state
  const decisionViewResult = useMemo(() => {
    if (viewState.type === 'weekly') return null
    const meetingNode = initNodes.find(n => n.id === viewState.meetingId)
    if (!meetingNode) return null

    // Compute anchor X from parent column position
    let anchorX
    if (meetingNode.parentId) {
      const parentNode = initNodes.find(n => n.id === meetingNode.parentId)
      if (parentNode) anchorX = parentNode.position.x + meetingNode.position.x
    } else {
      anchorX = meetingNode.position.x
    }

    // Get real decisions for this meeting (per D-05)
    const rawMeetingId = meetingNode.data.meetingId  // API UUID, per Pitfall 5
    const decisions = decisionsByMeeting[rawMeetingId] || []
    const meetingInfo = {
      title: meetingNode.data.title,
      day: meetingNode.data.day,
      startMinutes: meetingNode.data.startMinutes,
      endMinutes: meetingNode.data.endMinutes,
      attendees: meetingNode.data.attendees || [],
    }
    const allSeriesIds = chainLegend.map(c => c.seriesId)

    return buildDecisionViewData(
      viewState.meetingId, viewState.seriesId, decisions, meetingInfo, allSeriesIds, anchorX
    )
  }, [viewState, initNodes, decisionsByMeeting, chainLegend])

  // Re-fit view on resize
  useEffect(() => {
    const handleResize = () => {
      requestAnimationFrame(() => {
        reactFlow.fitView({ padding: FIT_VIEW_PADDING })
      })
    }
    window.addEventListener('resize', handleResize)
    return () => window.removeEventListener('resize', handleResize)
  }, [reactFlow])

  // Re-fit when viewMode or data changes
  useEffect(() => {
    if (initNodes.length > 0) {
      requestAnimationFrame(() => {
        reactFlow.fitView({ padding: FIT_VIEW_PADDING })
      })
    }
  }, [viewMode, initNodes.length, reactFlow])

  // Viewport transition effects
  useEffect(() => {
    if (viewState.type === 'transitioning_out' && decisionViewResult) {
      requestAnimationFrame(() => {
        const result = reactFlow.fitBounds(decisionViewResult.bounds, {
          duration: VIEWPORT_ANIMATION_DURATION,
          padding: DECISION_FIT_VIEW_PADDING,
        })
        if (result && typeof result.then === 'function') {
          result.then(() => {
            dispatch({ type: 'TRANSITION_OUT_COMPLETE' })
          })
        } else {
          // fitBounds may not return a promise in all xyflow versions
          setTimeout(() => {
            dispatch({ type: 'TRANSITION_OUT_COMPLETE' })
          }, VIEWPORT_ANIMATION_DURATION + 50)
        }
      })
    } else if (viewState.type === 'transitioning_in') {
      requestAnimationFrame(() => {
        const result = reactFlow.fitView({
          duration: VIEWPORT_ANIMATION_DURATION,
          padding: FIT_VIEW_PADDING,
          nodes: initNodes.map(n => ({ id: n.id })),
        })
        if (result && typeof result.then === 'function') {
          result.then(() => {
            dispatch({ type: 'TRANSITION_IN_COMPLETE' })
          })
        } else {
          setTimeout(() => {
            dispatch({ type: 'TRANSITION_IN_COMPLETE' })
          }, VIEWPORT_ANIMATION_DURATION + 50)
        }
      })
    }
  }, [viewState, reactFlow, initNodes, decisionViewResult])

  // Chain highlighting on nodes
  const displayNodes = useMemo(() => {
    if (!highlightedChainId) return initNodes
    return initNodes.map(node => {
      if (node.type !== 'meeting') return node
      const isHighlighted = node.data.seriesId === highlightedChainId
      return {
        ...node,
        data: { ...node.data, isHighlighted, isDimmed: !isHighlighted },
      }
    })
  }, [highlightedChainId, initNodes])

  // Chain highlighting on edges
  const displayEdges = useMemo(() => {
    if (!highlightedChainId) return initEdges
    return initEdges.map(edge => {
      const isHighlighted = edge.data?.seriesId === highlightedChainId
      return {
        ...edge,
        data: { ...edge.data, isHighlighted, isDimmed: !isHighlighted },
      }
    })
  }, [highlightedChainId, initEdges])

  // Combined nodes/edges with decision view overlay
  const combinedNodes = useMemo(() => {
    if (!decisionViewResult) return displayNodes
    return [...displayNodes, ...decisionViewResult.nodes]
  }, [displayNodes, decisionViewResult])

  const combinedEdges = useMemo(() => {
    if (!decisionViewResult) return displayEdges
    return [...displayEdges, ...decisionViewResult.edges]
  }, [displayEdges, decisionViewResult])

  // Meeting info for decision view header
  const clickedMeetingInfo = useMemo(() => {
    if (viewState.type !== 'transitioning_out' && viewState.type !== 'decision') return null
    const node = initNodes.find(n => n.id === viewState.meetingId)
    if (!node) return null
    const fmt = (m) => {
      const h = Math.floor(m / 60)
      const min = m % 60
      return `${h.toString().padStart(2, '0')}:${min.toString().padStart(2, '0')}`
    }
    return {
      title: node.data.title,
      day: node.data.day,
      time: `${fmt(node.data.startMinutes)} - ${fmt(node.data.endMinutes)}`,
      attendeeCount: (node.data.attendees || []).length,
      chainColor: node.data.chainColor || 'var(--accent-ochre-ink)',
    }
  }, [viewState, initNodes])

  const handleNodeMouseEnter = useCallback((_event, node) => {
    if (viewState.type !== 'weekly') return
    if (leaveTimerRef.current !== null) {
      cancelAnimationFrame(leaveTimerRef.current)
      leaveTimerRef.current = null
    }
    if (node.type === 'meeting' && node.data.seriesId) {
      setHighlightedChainId(node.data.seriesId)
    }
  }, [viewState.type])

  const handleNodeMouseLeave = useCallback(() => {
    if (viewState.type !== 'weekly') return
    leaveTimerRef.current = requestAnimationFrame(() => {
      setHighlightedChainId(null)
      leaveTimerRef.current = null
    })
  }, [viewState.type])

  const handleEdgeMouseEnter = useCallback((_event, edge) => {
    if (viewState.type !== 'weekly') return
    if (leaveTimerRef.current !== null) {
      cancelAnimationFrame(leaveTimerRef.current)
      leaveTimerRef.current = null
    }
    if (edge.data?.seriesId) {
      setHighlightedChainId(edge.data.seriesId)
    }
  }, [viewState.type])

  const handleEdgeMouseLeave = useCallback(() => {
    if (viewState.type !== 'weekly') return
    leaveTimerRef.current = requestAnimationFrame(() => {
      setHighlightedChainId(null)
      leaveTimerRef.current = null
    })
  }, [viewState.type])

  const handlePaneClick = useCallback(() => {
    if (viewState.type !== 'weekly') return
    setHighlightedChainId(null)
  }, [viewState.type])

  const handleToggleView = useCallback(() => {
    setViewMode(prev => prev === '3day' ? '5day' : '3day')
    setHighlightedChainId(null)
  }, [])

  const handleToggleScope = useCallback(() => {
    setScope(prev => prev === 'my' ? 'team' : 'my')
    setHighlightedChainId(null)
  }, [])

  // Decision drill-down on node click
  const handleNodeClick = useCallback((_event, node) => {
    if (node.type === 'meeting') {
      if (node.data.isGhost) return
      if (!node.data.seriesId) return
      dispatch({ type: 'CLICK_MEETING', meetingId: node.id, seriesId: node.data.seriesId })
    }
  }, [])

  const handleBack = useCallback(() => {
    dispatch({ type: 'GO_BACK' })
  }, [])

  // Escape key listener for decision view
  useEffect(() => {
    const handleKeyDown = (e) => {
      if (e.key === 'Escape' && viewState.type === 'decision') {
        handleBack()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [viewState.type, handleBack])

  const handleRetry = useCallback(() => {
    setError(null)
    setLoading(true)
    getFlowData(undefined, undefined, scope)
      .then(apiData => setFlowData(apiData))
      .catch(err => setError(err.message || t('flows.loadError')))
      .finally(() => setLoading(false))
  }, [scope, t])

  // Loading state
  if (loading) {
    return (
      <div className="meeting-flow-wrapper flex items-center justify-center">
        <LoadingState message={t('flows.loading')} />
      </div>
    )
  }

  // Error state
  if (error) {
    return (
      <div className="meeting-flow-wrapper flex items-center justify-center">
        <ErrorState message={error} onRetry={handleRetry} />
      </div>
    )
  }

  // Empty state - check if there are any meeting nodes (not day columns)
  const meetingNodes = initNodes.filter(n => n.type === 'meeting')
  if (!flowData || meetingNodes.length === 0) {
    return (
      <div className="meeting-flow-wrapper flex items-center justify-center">
        <EmptyState
          title={t('flows.noMeetings')}
          description={t('flows.noMeetingsDesc')}
        />
      </div>
    )
  }

  return (
    <div className="meeting-flow-wrapper">
      {(viewState.type === 'weekly' || viewState.type === 'transitioning_in') && (
        <div className="flow-toolbar">
          <button className="view-toggle-btn" onClick={handleToggleView}>
            {viewMode === '3day' ? t('flows.fiveDay') : t('flows.threeDay')}
          </button>
          <button
            className={`scope-toggle-btn ${scope === 'team' ? 'scope-toggle-btn--active' : ''}`}
            onClick={handleToggleScope}
          >
            {scope === 'my' ? t('flows.teamView') : t('flows.myMeetings')}
          </button>
        </div>
      )}
      <ReactFlow
        nodes={combinedNodes}
        edges={combinedEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        defaultEdgeOptions={{ type: 'chain' }}
        fitView
        fitViewOptions={{ padding: FIT_VIEW_PADDING }}
        panOnDrag={false}
        panOnScroll={false}
        zoomOnScroll={false}
        zoomOnPinch={false}
        zoomOnDoubleClick={false}
        nodesDraggable={false}
        nodesConnectable={false}
        proOptions={{ hideAttribution: true }}
        onNodeMouseEnter={handleNodeMouseEnter}
        onNodeMouseLeave={handleNodeMouseLeave}
        onEdgeMouseEnter={handleEdgeMouseEnter}
        onEdgeMouseLeave={handleEdgeMouseLeave}
        onPaneClick={handlePaneClick}
        onNodeClick={handleNodeClick}
      >
        <Background variant={BackgroundVariant.Dots} gap={24} size={1} color="rgba(45, 43, 39, 0.08)" />
      </ReactFlow>
      {clickedMeetingInfo && (
        <DecisionViewHeader
          meetingTitle={clickedMeetingInfo.title}
          meetingDay={clickedMeetingInfo.day}
          meetingTime={clickedMeetingInfo.time}
          attendeeCount={clickedMeetingInfo.attendeeCount}
          chainColor={clickedMeetingInfo.chainColor}
          onBack={handleBack}
          visible={viewState.type === 'decision'}
        />
      )}
      {(viewState.type === 'weekly' || viewState.type === 'transitioning_in') && chainLegend && chainLegend.length > 0 && (
        <div className="chain-legend">
          {chainLegend.map(chain => (
            <div
              key={chain.seriesId}
              className={`chain-legend-item ${highlightedChainId === chain.seriesId ? 'chain-legend-item--active' : ''}`}
              onClick={() => setHighlightedChainId(prev => prev === chain.seriesId ? null : chain.seriesId)}
            >
              <div className="chain-legend-dot" style={{ backgroundColor: chain.color }} />
              {chain.name}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

export default function MeetingFlow() {
  return (
    <ReactFlowProvider>
      <MeetingFlowInner />
    </ReactFlowProvider>
  )
}
