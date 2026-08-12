import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { clippedText } from '~/styles/shared.css'
import { classSelector } from '~/test-support/composedClass'
import { dragPreviewTooltip } from './AppShell.css'
import { renderSidebarTabOverlay } from './TabDragContext'

const LONG_TITLE = 'a-terminal-tab-title-far-wider-than-the-drag-preview-can-show'

describe('renderSidebarTabOverlay', () => {
  it('shows the tab title', () => {
    const { container } = render(() => renderSidebarTabOverlay('My tab'))
    expect(container.textContent).toBe('My tab')
  })

  it('wears the shared drag-preview chrome instead of an inline copy', () => {
    const { container } = render(() => renderSidebarTabOverlay('My tab'))
    expect(container.querySelector(classSelector(dragPreviewTooltip))).toBeTruthy()
  })

  /**
   * The clip belongs to the LABEL, not to the box around it.
   *
   * The inline copy this replaced put `text-overflow: ellipsis` on the flex
   * container, where the property does nothing -- it acts on a block
   * container's own inline content, never on a flex item. A long title was
   * therefore cut with no ellipsis to mark it.
   */
  it('puts the clip on the label, not on the flex container', () => {
    const { container } = render(() => renderSidebarTabOverlay(LONG_TITLE))

    const box = container.querySelector(classSelector(dragPreviewTooltip))!
    expect(box.className).not.toMatch(/clippedText/)

    const label = container.querySelector(classSelector(clippedText))!
    expect(label.tagName).toBe('SPAN')
    expect(label.textContent).toBe(LONG_TITLE)
  })
})
