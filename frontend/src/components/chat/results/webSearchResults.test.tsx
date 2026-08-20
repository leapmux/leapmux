import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { clippedText } from '~/styles/shared.css'
import { hoverForTooltip, stubClipped, stubFitting } from '~/test-support/clipStub'
import { classSelector } from '~/test-support/composedClass'
import { toolMetaRow } from '../toolStyles.css'
import { WebSearchResultsBody } from './webSearchResults'

const LONG_TITLE = 'How to configure a reverse proxy for a self-hosted service without breaking websockets'

function renderResults(title = LONG_TITLE) {
  return render(() => (
    <WebSearchResultsBody
      source={{
        links: [{ title, url: 'https://example.com/a/deep/path' }],
        summary: '',
      }}
    />
  ))
}

describe('webSearchResultsBody', () => {
  // The link row and the Agent card's meta row are one primitive, and this file
  // held a byte-identical copy of it. Pinning the shared class here is what stops
  // the next renderer from forking a third.
  it('puts each link on the shared tool meta row', () => {
    const { container } = renderResults()
    expect(container.querySelector(classSelector(toolMetaRow))).toBeTruthy()
  })

  it('renders the link title and its domain', () => {
    const { container } = renderResults()
    expect(container.textContent).toContain(LONG_TITLE)
    expect(container.textContent).toContain('example.com')
  })

  /**
   * A search-result title runs far past this panel, so the ellipsis is
   * guaranteed and the tooltip is the only route to the rest.
   *
   * The clip stays on the SPAN, not on the <a> it holds: an inline
   * non-replaced box ignores `overflow`, so moving it would lose the ellipsis.
   * That is also why this site keeps a hand-built `Tooltip` instead of
   * `ClippedText`, which renders a plain string and cannot hold the link.
   */
  describe('title clipping', () => {
    beforeEach(() => {
      vi.useFakeTimers()
    })

    afterEach(() => {
      vi.useRealTimers()
      vi.restoreAllMocks()
    })

    function labelOf(container: HTMLElement): HTMLElement {
      return container.querySelector<HTMLElement>(classSelector(clippedText))!
    }

    it('keeps the clip on the span that holds the link', () => {
      const { container } = renderResults()
      const label = labelOf(container)
      expect(label.tagName).toBe('SPAN')
      expect(label.querySelector('a')).toBeTruthy()
      expect(label.querySelector('a')!.className).not.toMatch(/clippedText/)
    })

    it('gives the full title on hover once it is clipped', () => {
      const { container } = renderResults()
      const label = labelOf(container)
      stubClipped(label)
      expect(hoverForTooltip(label)?.textContent).toBe(LONG_TITLE)
    })

    it('shows no tooltip while the title fits', () => {
      const { container } = renderResults('Short')
      const label = labelOf(container)
      stubFitting(label)
      expect(hoverForTooltip(label)).toBeNull()
    })
  })
})
