import type { SpanBridgeEntry } from './SpanLineGapBridges'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { BAND_BORDER_PX } from '../chatRowGeometry'
import { SpanLineGapBridges } from './SpanLineGapBridges'
import { SPAN_BRIDGE_GAP_VAR } from './SpanLines.geometry'

describe('spanlinegapbridges', () => {
  // Two rows sharing one active span column: the SECOND row's column continues from the
  // first (an 'active' line has both a vertical top and bottom with the same key), so its
  // gap bridge connects. The first row has nothing above it, so it never draws a bridge.
  const entry = (id: string): SpanBridgeEntry => ({
    msg: { id, spanId: 's1' },
    parsedSpanLines: [{ type: 'active', span_id: 's1' }],
    category: { kind: 'assistant_text' },
  })

  it('draws a bridge for a connecting row but not for the first (unconnected) row', () => {
    const entries = [entry('a'), entry('b')]
    const { container } = render(() => (
      <SpanLineGapBridges
        entries={entries}
        precedingEntry={undefined}
        topOf={() => 0}
        hiddenOf={() => false}
        gapAboveOf={() => 8}
      />
    ))
    expect(container.querySelector('[data-span-gap-bridges-for="a"]')).toBeNull()
    expect(container.querySelector('[data-span-gap-bridges-for="b"]')).not.toBeNull()
  })

  it('hides a row bridge exactly when hiddenOf(id) is true (the skeleton case)', () => {
    // Models a fling skeleton: while the row shows a placeholder (no span column) its
    // bridge must hide, then reappear when the real row upgrades in.
    const [hidden, setHidden] = createSignal(false)
    const entries = [entry('a'), entry('b')]
    const { container } = render(() => (
      <SpanLineGapBridges
        entries={entries}
        precedingEntry={undefined}
        topOf={() => 0}
        hiddenOf={id => id === 'b' && hidden()}
        gapAboveOf={() => 8}
      />
    ))
    const bridgeB = () => container.querySelector('[data-span-gap-bridges-for="b"]') as HTMLElement
    expect(bridgeB().style.visibility).toBe('') // real content -> visible
    setHidden(true) // row 'b' becomes a skeleton
    expect(bridgeB().style.visibility).toBe('hidden')
    setHidden(false) // row 'b' upgrades to real
    expect(bridgeB().style.visibility).toBe('')
  })

  it('sizes its segments from the gap the offset map actually left above the row', () => {
    // The bridge used to restate the gap as a token of its own, which held only while
    // every gap was the same. Two adjacent BANDS overlap by a border width instead, so a
    // segment sized from a token was built for a gap that does not exist. Reading the
    // offset map's own decider collapses it, and keeps one function in charge of both a
    // row's offset and the rail that spans the space above it.
    const entries = [entry('a'), entry('b')]
    const render8 = render(() => (
      <SpanLineGapBridges
        entries={entries}
        precedingEntry={undefined}
        topOf={() => 0}
        hiddenOf={() => false}
        gapAboveOf={() => 8}
      />
    ))
    const anchor8 = render8.container.querySelector('[data-span-gap-bridges-for="b"]') as HTMLElement
    expect(anchor8.style.getPropertyValue(SPAN_BRIDGE_GAP_VAR)).toBe('8px')
    render8.unmount()

    const merged = render(() => (
      <SpanLineGapBridges
        entries={entries}
        precedingEntry={undefined}
        topOf={() => 0}
        hiddenOf={() => false}
        gapAboveOf={() => -BAND_BORDER_PX}
      />
    ))
    const anchorMerged = merged.container.querySelector('[data-span-gap-bridges-for="b"]') as HTMLElement
    expect(anchorMerged.style.getPropertyValue(SPAN_BRIDGE_GAP_VAR)).toBe(`${-BAND_BORDER_PX}px`)
  })
})
