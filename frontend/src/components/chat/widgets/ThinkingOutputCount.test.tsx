/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { SHOW_DELAY_MS } from '~/components/common/Tooltip'
import { ThinkingOutputCount } from './ThinkingOutputCount'

describe('thinking output count', () => {
  it('formats byte-unit transitions', () => {
    for (const [bytes, label] of [
      [1023, '1023 B'],
      [1024, '1.0 KB'],
      [1024 * 1024, '1.0 MB'],
    ] as const) {
      const { getByText, unmount } = render(() => <ThinkingOutputCount bytes={bytes} />)
      expect(getByText(label)).toBeInTheDocument()
      unmount()
    }
  })

  it('marks a minimum count', () => {
    const { getByText } = render(() => <ThinkingOutputCount bytes={1536} minimum />)
    expect(getByText('≥1.5 KB')).toBeInTheDocument()
  })

  it('explains a minimum count in the app tooltip', () => {
    vi.useFakeTimers()
    try {
      const { container, unmount } = render(() => <ThinkingOutputCount bytes={1536} minimum />)
      fireEvent.mouseEnter(container.querySelector('[data-animated-count]')!)
      vi.advanceTimersByTime(SHOW_DELAY_MS)
      expect(screen.getByRole('tooltip', { hidden: true }))
        .toHaveTextContent('The provider limits live output. This count is a minimum.')
      unmount()
    }
    finally {
      vi.useRealTimers()
    }
  })
})
