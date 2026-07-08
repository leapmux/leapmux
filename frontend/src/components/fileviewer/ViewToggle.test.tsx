import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ViewToggle } from '~/components/fileviewer/ViewToggle'

// Mock FileViewer.css to provide minimal class names
vi.mock('~/components/fileviewer/FileViewer.css', () => ({
  viewToggle: 'viewToggle',
  viewToggleButton: 'viewToggleButton',
  viewToggleActive: 'viewToggleActive',
}))

function noop() {}

describe('viewToggle', () => {
  it('renders the render / source toggle buttons', () => {
    render(() => <ViewToggle mode="render" onToggle={noop} />)
    // The render toggle button (Eye icon) is the first toggle in the row.
    expect(screen.getAllByRole('button').length).toBeGreaterThanOrEqual(2)
  })

  it('shows the side-by-side toggle when showSplit is true', () => {
    const { container } = render(() => (
      <ViewToggle mode="render" onToggle={noop} showSplit />
    ))
    // Three toggle buttons total: render, split, source.
    expect(container.querySelectorAll('button').length).toBe(3)
  })

  it('calls onToggle when a toggle button is clicked', () => {
    const onToggle = vi.fn()
    const { container } = render(() => (
      <ViewToggle mode="render" onToggle={onToggle} showSplit />
    ))
    const buttons = container.querySelectorAll('button')
    buttons[1].click() // split
    expect(onToggle).toHaveBeenCalledWith('split')
  })
})
