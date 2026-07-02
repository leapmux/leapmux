import type { SpanBridgeEntry } from './SpanLineGapBridges'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { SpanLineGapBridges } from './SpanLineGapBridges'

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
      />
    ))
    const bridgeB = () => container.querySelector('[data-span-gap-bridges-for="b"]') as HTMLElement
    expect(bridgeB().style.visibility).toBe('') // real content -> visible
    setHidden(true) // row 'b' becomes a skeleton
    expect(bridgeB().style.visibility).toBe('hidden')
    setHidden(false) // row 'b' upgrades to real
    expect(bridgeB().style.visibility).toBe('')
  })
})
