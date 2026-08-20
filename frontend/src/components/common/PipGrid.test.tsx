import { cleanup, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { PIP_GRID_COLUMNS, PIP_GRID_PIPS, PipGrid } from './PipGrid'

afterEach(cleanup)

/** Nine fills a reader can tell apart by index, so a mis-ordered map shows up. */
const NUMBERED = Array.from({ length: PIP_GRID_PIPS }, (_, i) => `#00000${i}`)

function pipsOf(container: HTMLElement): SVGRectElement[] {
  return [...container.querySelectorAll('rect')]
}

describe('pipGrid', () => {
  it('renders one rect per pip', () => {
    const { container } = render(() => <PipGrid fills={NUMBERED} />)
    expect(pipsOf(container)).toHaveLength(PIP_GRID_PIPS)
  })

  it('applies the fills row-major', () => {
    const { container } = render(() => <PipGrid fills={NUMBERED} />)
    expect(pipsOf(container).map(r => r.getAttribute('fill'))).toEqual(NUMBERED)
  })

  it('lays the pips out on a 3x3 lattice with a one-unit gap', () => {
    const { container } = render(() => <PipGrid fills={NUMBERED} />)
    const placed = pipsOf(container).map(r => `${r.getAttribute('x')},${r.getAttribute('y')}`)
    // Row-major, so index 4 is row 1 column 1 and both coordinates step once.
    // A pip is 3 wide and the step is 4, which leaves the 1-unit gap.
    expect(placed).toEqual(['0,0', '4,0', '8,0', '0,4', '4,4', '8,4', '0,8', '4,8', '8,8'])
    for (const rect of pipsOf(container)) {
      expect(rect.getAttribute('width')).toBe('3')
      expect(rect.getAttribute('height')).toBe('3')
      expect(rect.getAttribute('rx')).toBe('0.5')
    }
  })

  it('sizes the viewBox to the pips with no outer padding', () => {
    const { container } = render(() => <PipGrid fills={NUMBERED} />)
    // Three pips of 3 and the two 1-unit gaps between them. The e2e suite and
    // ContextUsageGrid's own geometry both depend on this staying 11.
    expect(container.querySelector('svg')?.getAttribute('viewBox')).toBe('0 0 11 11')
    expect(PIP_GRID_COLUMNS).toBe(3)
  })

  it('sets width and height from size', () => {
    const { container } = render(() => <PipGrid fills={NUMBERED} size={12} />)
    const svg = container.querySelector('svg')!
    expect(svg.getAttribute('width')).toBe('12')
    expect(svg.getAttribute('height')).toBe('12')
  })

  it('leaves width and height off when size is absent, so CSS can size it', () => {
    const { container } = render(() => <PipGrid fills={NUMBERED} />)
    const svg = container.querySelector('svg')!
    expect(svg.getAttribute('width')).toBeNull()
    expect(svg.getAttribute('height')).toBeNull()
  })

  it('passes class and testId through to the svg', () => {
    const { container } = render(() => <PipGrid fills={NUMBERED} class="chip" testId="pips" />)
    const svg = container.querySelector('svg')!
    expect(svg.getAttribute('class')).toBe('chip')
    expect(svg.getAttribute('data-testid')).toBe('pips')
  })

  it('omits data-testid when no testId is given', () => {
    const { container } = render(() => <PipGrid fills={NUMBERED} />)
    expect(container.querySelector('svg')!.hasAttribute('data-testid')).toBe(false)
  })

  it('still renders nine pips when given too few fills, and paints none of them black', () => {
    // SVG paints a rect with no `fill` attribute BLACK, so a short array would
    // put colours in the grid that no caller asked for.
    const { container } = render(() => <PipGrid fills={['#010101', '#020202']} />)
    const fills = pipsOf(container).map(r => r.getAttribute('fill'))
    expect(fills).toHaveLength(PIP_GRID_PIPS)
    expect(fills.slice(0, 2)).toEqual(['#010101', '#020202'])
    expect(fills.slice(2)).toEqual(Array.from<string>({ length: 7 }).fill('transparent'))
  })

  it('ignores fills past the ninth', () => {
    const tooMany = [...NUMBERED, '#ff0000', '#00ff00']
    const { container } = render(() => <PipGrid fills={tooMany} />)
    expect(pipsOf(container)).toHaveLength(PIP_GRID_PIPS)
    expect(pipsOf(container).map(r => r.getAttribute('fill'))).toEqual(NUMBERED)
  })

  it('renders nine transparent pips for an empty fill list', () => {
    const { container } = render(() => <PipGrid fills={[]} />)
    const fills = pipsOf(container).map(r => r.getAttribute('fill'))
    expect(fills).toEqual(Array.from<string>({ length: PIP_GRID_PIPS }).fill('transparent'))
  })

  it('accepts css variables as fills', () => {
    const vars = Array.from<string>({ length: PIP_GRID_PIPS }).fill('var(--border)')
    const { container } = render(() => <PipGrid fills={vars} />)
    expect(pipsOf(container).map(r => r.getAttribute('fill'))).toEqual(vars)
  })

  it('re-fills the same rects when the fills change', () => {
    const [fills, setFills] = createSignal<readonly string[]>(NUMBERED)
    const { container } = render(() => <PipGrid fills={fills()} />)
    const before = pipsOf(container)

    const repainted = Array.from<string>({ length: PIP_GRID_PIPS }).fill('#ffffff')
    setFills(repainted)

    expect(pipsOf(container).map(r => r.getAttribute('fill'))).toEqual(repainted)
    // The rects are keyed by position, not by fill, so a repaint must not
    // replace the elements. ContextUsageGrid re-fills them on every token
    // update, and swapping nine nodes each time would churn the DOM.
    expect(pipsOf(container)).toEqual(before)
  })
})
