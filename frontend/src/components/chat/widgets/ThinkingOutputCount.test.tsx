/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
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
})
