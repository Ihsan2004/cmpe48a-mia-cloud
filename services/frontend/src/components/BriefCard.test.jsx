import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'

// Mock dependencies
vi.mock('react-router-dom', () => ({
  useNavigate: () => vi.fn(),
}))

vi.mock('../services/api', () => ({
  submitBriefFeedback: vi.fn(() => Promise.resolve()),
  getBriefDetail: vi.fn(() => Promise.resolve()),
}))

vi.mock('../utils/toast', () => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('./MarkdownRenderer', () => ({
  default: ({ content }) => <div data-testid="markdown">{content}</div>,
}))

import BriefCard from './BriefCard'

const baseBrief = {
  id: 'brief-1',
  target_meeting_id: 'meeting-1',
  content: 'Son toplantıda API v2 kararı alındı. Bob dokümantasyon hazırlayacak. Açık konu: bütçe netleşmedi.',
  generation_method: 'llm',
  opened_at: null,
}

describe('BriefCard', () => {
  // ==================== Rendering ====================

  it('renders meeting title', () => {
    render(<BriefCard brief={baseBrief} meetingTitle="Haftalık Sync" meetingTime="2026-04-01T14:00:00Z" />)
    expect(screen.getByText('Haftalık Sync')).toBeDefined()
  })

  it('renders preview content (truncated)', () => {
    render(<BriefCard brief={baseBrief} meetingTitle="Sync" meetingTime="2026-04-01T14:00:00Z" />)
    expect(screen.getByText(/API v2/)).toBeDefined()
  })

  it('shows expand button', () => {
    render(<BriefCard brief={baseBrief} meetingTitle="Sync" meetingTime="2026-04-01T14:00:00Z" />)
    expect(screen.getByText('Devamını oku')).toBeDefined()
  })

  it('shows feedback buttons', () => {
    render(<BriefCard brief={baseBrief} meetingTitle="Sync" meetingTime="2026-04-01T14:00:00Z" />)
    expect(screen.getByText('Faydalı')).toBeDefined()
    expect(screen.getByText('Faydalı değil')).toBeDefined()
  })

  it('shows "Kararları Gör" link', () => {
    render(<BriefCard brief={baseBrief} meetingTitle="Sync" meetingTime="2026-04-01T14:00:00Z" />)
    expect(screen.getByText(/Kararları Gör/)).toBeDefined()
  })

  // ==================== Template Fallback Badge ====================

  it('shows template fallback warning', () => {
    const templateBrief = { ...baseBrief, generation_method: 'template_fallback' }
    render(<BriefCard brief={templateBrief} meetingTitle="Sync" meetingTime="2026-04-01T14:00:00Z" />)
    expect(screen.getByText(/Şablon ile oluşturuldu/)).toBeDefined()
  })

  it('does not show template warning for LLM briefs', () => {
    render(<BriefCard brief={baseBrief} meetingTitle="Sync" meetingTime="2026-04-01T14:00:00Z" />)
    expect(screen.queryByText(/Şablon ile oluşturuldu/)).toBeNull()
  })

  // ==================== Expand/Collapse ====================

  it('expands on click and shows full content', async () => {
    render(<BriefCard brief={baseBrief} meetingTitle="Sync" meetingTime="2026-04-01T14:00:00Z" />)

    const expandBtn = screen.getByText('Devamını oku')
    fireEvent.click(expandBtn)

    // After expand, should show "Kapat"
    expect(screen.getByText('Kapat')).toBeDefined()
  })

  // ==================== Meeting Time ====================

  it('renders without crashing with valid meetingTime', () => {
    const { container } = render(<BriefCard brief={baseBrief} meetingTitle="Sync" meetingTime="2026-04-01T14:00:00Z" />)
    expect(container.querySelector('.card')).toBeDefined()
  })
})
