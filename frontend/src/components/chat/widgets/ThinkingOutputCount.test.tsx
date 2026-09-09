/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ThinkingOutputCount } from './ThinkingOutputCount'

describe('thinking output count', () => {
  it('formats exact byte counts', () => {
    const { getByText } = render(() => <ThinkingOutputCount bytes={1536} />)
    expect(getByText('1.5 KB')).toBeInTheDocument()
  })

  it('marks a minimum count', () => {
    const { getByText } = render(() => <ThinkingOutputCount bytes={1536} minimum />)
    expect(getByText('≥1.5 KB')).toBeInTheDocument()
  })
})
