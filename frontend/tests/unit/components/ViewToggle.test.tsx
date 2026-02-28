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
  it('shows mention button when onMention is provided', () => {
    render(() => (
      <ViewToggle
        mode="render"
        onToggle={noop}
        onMention={() => {}}
      />
    ))
    expect(screen.getByTestId('file-mention-button')).toBeTruthy()
  })

  it('hides mention button when onMention is undefined', () => {
    render(() => (
      <ViewToggle
        mode="render"
        onToggle={noop}
      />
    ))
    expect(screen.queryByTestId('file-mention-button')).toBeNull()
  })

  it('calls onMention when mention button is clicked', () => {
    const onMention = vi.fn()
    render(() => (
      <ViewToggle
        mode="render"
        onToggle={noop}
        onMention={onMention}
      />
    ))
    screen.getByTestId('file-mention-button').click()
    expect(onMention).toHaveBeenCalledOnce()
  })
})
