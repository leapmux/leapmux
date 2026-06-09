/// <reference types="vitest/globals" />
import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { forwardDelta, shapeFamily, ThinkingTokenCount } from './ThinkingTokenCount'
import * as styles from './ThinkingTokenCount.css'

// The visible odometer reads data-digit off each rolling column, so tests can
// assert the displayed value without depending on CSS transforms. Columns are
// laid out right-to-left (row-reverse), so reverse the DOM order to read them in
// most-significant-first order.
function visibleDigits(container: HTMLElement): string {
  return Array.from(container.querySelectorAll('[data-testid="odo-digit"]'))
    .map(el => el.getAttribute('data-digit'))
    .reverse()
    .join('')
}

describe('forwardDelta', () => {
  it('advances forward through the 9->0 wrap, never backward', () => {
    expect(forwardDelta(9, 0)).toBe(1) // the carry: up through the wrap, not -9
    expect(forwardDelta(2, 5)).toBe(3)
    expect(forwardDelta(7, 2)).toBe(5) // 7->8->9->0->1->2
    expect(forwardDelta(5, 5)).toBe(0)
    expect(forwardDelta(0, 9)).toBe(9)
  })
})

describe('shapeFamily', () => {
  it('groups values by unit so same-unit growth shares a family', () => {
    // Bare integers are one family, regardless of digit count.
    expect(shapeFamily('99')).toBe('')
    expect(shapeFamily('100')).toBe('')
    expect(shapeFamily('999')).toBe('')
    // k is its own family across the column-growth boundary.
    expect(shapeFamily('1.0k')).toBe('k')
    expect(shapeFamily('9.9k')).toBe('k')
    expect(shapeFamily('10.0k')).toBe('k')
    expect(shapeFamily('999.9k')).toBe('k')
    // M likewise.
    expect(shapeFamily('1.0M')).toBe('M')
  })

  it('changes family only when the unit changes (the crossfade trigger)', () => {
    expect(shapeFamily('999') === shapeFamily('1.0k')).toBe(false) // integer -> k
    expect(shapeFamily('999.9k') === shapeFamily('1.0M')).toBe(false) // k -> M
    expect(shapeFamily('9.9k') === shapeFamily('10.0k')).toBe(true) // growth within k
  })
})

describe('thinking token count', () => {
  it('exposes the formatted value as accessible text', () => {
    const { getByText } = render(() => <ThinkingTokenCount tokens={230} />)
    expect(getByText('230 tokens')).toBeInTheDocument()
  })

  it('compacts large values in the accessible text, to two decimals', () => {
    const { getByText } = render(() => <ThinkingTokenCount tokens={1234} />)
    expect(getByText('1.23k tokens')).toBeInTheDocument()
  })

  it('renders one rolling column per digit of the formatted value', () => {
    const { container } = render(() => <ThinkingTokenCount tokens={230} />)
    expect(visibleDigits(container)).toBe('230')
  })

  it('rolls digits in place while the digit-shape is stable', () => {
    const [tokens, setTokens] = createSignal(230)
    const { container, queryAllByTestId } = render(() => <ThinkingTokenCount tokens={tokens()} />)
    expect(visibleDigits(container)).toBe('230')

    setTokens(265)

    expect(visibleDigits(container)).toBe('265')
    // Same family => roll, not crossfade: no outgoing snapshot layer.
    expect(queryAllByTestId('odo-exiting')).toHaveLength(0)
  })

  it('grows a new leading column without crossfading when the unit is unchanged', () => {
    const [tokens, setTokens] = createSignal(99)
    const { container, queryAllByTestId } = render(() => <ThinkingTokenCount tokens={tokens()} />)
    expect(visibleDigits(container)).toBe('99')

    setTokens(100) // "99" -> "100": same family (bare integer), one digit longer

    expect(visibleDigits(container)).toBe('100')
    expect(queryAllByTestId('odo-digit')).toHaveLength(3) // grew a column
    expect(queryAllByTestId('odo-exiting')).toHaveLength(0) // no crossfade
  })

  it('grows a leading digit within the k family, around the decimal/unit', () => {
    const [tokens, setTokens] = createSignal(9900)
    const { container, queryAllByTestId } = render(() => <ThinkingTokenCount tokens={tokens()} />)
    expect(visibleDigits(container)).toBe('990') // "9.90k"

    setTokens(10000) // "9.90k" -> "10.00k": same family, new leading digit

    expect(visibleDigits(container)).toBe('1000') // "10.00k"
    expect(queryAllByTestId('odo-digit')).toHaveLength(4) // 9,9,0 -> 1,0,0,0
    expect(queryAllByTestId('odo-exiting')).toHaveLength(0) // grow, don't crossfade
  })

  it('crossfades a fading-out snapshot when the unit family changes', async () => {
    const [tokens, setTokens] = createSignal(999)
    const { queryAllByTestId } = render(() => <ThinkingTokenCount tokens={tokens()} />)
    expect(queryAllByTestId('odo-exiting')).toHaveLength(0)

    setTokens(1234) // "999" (integer) -> "1.23k" (k): unit family changes

    await waitFor(() => expect(queryAllByTestId('odo-exiting')).toHaveLength(1))
    // The snapshot is removed once its fade completes.
    await waitFor(() => expect(queryAllByTestId('odo-exiting')).toHaveLength(0))
  })

  it('fades the outgoing snapshot as a whole, not digit-by-digit', async () => {
    const [tokens, setTokens] = createSignal(999)
    const { getByTestId, queryAllByTestId } = render(() => <ThinkingTokenCount tokens={tokens()} />)

    setTokens(1234) // "999" -> "1.23k": family change -> crossfade

    await waitFor(() => expect(queryAllByTestId('odo-exiting')).toHaveLength(1))
    const snapshotDigits = getByTestId('odo-exiting').querySelectorAll('[data-testid="odo-digit"]')
    expect(snapshotDigits.length).toBeGreaterThan(0)
    // The snapshot's digits must not carry the entry fade — only the layer fades
    // out. Otherwise the old digits would fade in while the layer fades out.
    for (const digit of snapshotDigits)
      expect(digit.classList.contains(styles.slotEnter)).toBe(false)
  })
})
