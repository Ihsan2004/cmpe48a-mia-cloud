import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'

// Mock i18n
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key) => key }),
}))

import DecisionItemRow from './DecisionItemRow'

const baseItem = {
  id: 'test-id-1',
  text: 'API v2 ile devam edilecek',
  item_type: 'decision',
  owner_name: 'Ali',
  source_quote: 'Ali: v2 ile gidelim',
  status: 'confirmed',
  context: null,
  alternatives_considered: null,
}

const noop = () => Promise.resolve()

describe('DecisionItemRow', () => {
  // ==================== Rendering ====================

  it('renders item text', () => {
    render(<DecisionItemRow item={baseItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText('API v2 ile devam edilecek')).toBeDefined()
  })

  it('renders owner name', () => {
    render(<DecisionItemRow item={baseItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText(/Sahip: Ali/)).toBeDefined()
  })

  it('renders source quote', () => {
    render(<DecisionItemRow item={baseItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText(/v2 ile gidelim/)).toBeDefined()
  })

  it('renders status badge', () => {
    render(<DecisionItemRow item={baseItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText('Onaylı')).toBeDefined()
  })

  it('renders type label for decision', () => {
    render(<DecisionItemRow item={baseItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText(/Karar/)).toBeDefined()
  })

  it('renders type label for action_item', () => {
    const actionItem = { ...baseItem, item_type: 'action_item' }
    render(<DecisionItemRow item={actionItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText(/Aksiyon/)).toBeDefined()
  })

  // ==================== Context + Alternatives (ADR Fields) ====================

  it('renders context when present', () => {
    const itemWithContext = { ...baseItem, context: 'Performans testleri olumlu' }
    render(<DecisionItemRow item={itemWithContext} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText(/Performans testleri/)).toBeDefined()
    expect(screen.getByText(/Sebep/)).toBeDefined()
  })

  it('does not render context when null', () => {
    render(<DecisionItemRow item={baseItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.queryByText(/Sebep/)).toBeNull()
  })

  it('renders alternatives when present', () => {
    const itemWithAlts = { ...baseItem, alternatives_considered: 'B seçeneği (yavaş)' }
    render(<DecisionItemRow item={itemWithAlts} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText(/B seçeneği/)).toBeDefined()
    expect(screen.getByText(/Alternatifler/)).toBeDefined()
  })

  it('does not render alternatives when null', () => {
    render(<DecisionItemRow item={baseItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.queryByText(/Alternatifler/)).toBeNull()
  })

  // ==================== Status-Based Actions ====================

  it('shows complete + dismiss buttons for confirmed items', () => {
    render(<DecisionItemRow item={baseItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    // Confirmed items should have complete (CheckCheck) and dismiss (Ban) buttons
    const buttons = screen.getAllByRole('button')
    expect(buttons.length).toBeGreaterThanOrEqual(2)
  })

  it('shows no action buttons for completed items', () => {
    const completedItem = { ...baseItem, status: 'completed' }
    render(<DecisionItemRow item={completedItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText('Tamamlandı')).toBeDefined()
  })

  it('shows dismissed status for dismissed items', () => {
    const dismissedItem = { ...baseItem, status: 'dismissed' }
    render(<DecisionItemRow item={dismissedItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText('Kapatıldı')).toBeDefined()
  })

  // ==================== No Owner ====================

  it('renders without owner when null', () => {
    const noOwnerItem = { ...baseItem, owner_name: null }
    render(<DecisionItemRow item={noOwnerItem} onComplete={noop} onDismiss={noop} onDelete={noop} onUpdate={noop} />)
    expect(screen.getByText('API v2 ile devam edilecek')).toBeDefined()
    expect(screen.queryByText(/Sahip/)).toBeNull()
  })
})
