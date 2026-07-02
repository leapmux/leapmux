import type { SpanBridgeEntry } from '~/components/chat/widgets/SpanLineGapBridges'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { SpanLineGapBridges } from '~/components/chat/widgets/SpanLineGapBridges'
import { bodySpanKey, shouldConnectSpanLineTop, SpanLines } from '~/components/chat/widgets/SpanLines'
import { LINE_THICKNESS, spanColumnCenterX } from '~/components/chat/widgets/SpanLines.geometry'

describe('spanLines', () => {
  it('renders nothing when lines array is empty', () => {
    const { container } = render(() => (
      <SpanLines lines={[]} />
    ))
    // The <Show> guard prevents rendering when lines is empty.
    expect(container).toBeEmptyDOMElement()
  })

  it('renders a container with one column per line entry', () => {
    const { container } = render(() => (
      <SpanLines
        lines={[{ span_id: 'span-A', color: 0, type: 'active' }]}
        spanOpener
      />
    ))
    // Should render a container div with one child (the column).
    const wrapper = container.firstElementChild
    expect(wrapper).toBeInTheDocument()
    expect(wrapper!.children.length).toBe(1)
  })

  it('renders correct number of columns with null entries', () => {
    const { container } = render(() => (
      <SpanLines
        lines={[{ span_id: 'span-A', color: 0, type: 'active' }, null, { span_id: 'span-B', color: 1, type: 'active' }]}
        spanOpener
      />
    ))
    const wrapper = container.firstElementChild
    expect(wrapper).toBeInTheDocument()
    // 3 columns: connected, empty, active
    expect(wrapper!.children.length).toBe(3)
  })

  it('renders columns for parallel spans', () => {
    const { container } = render(() => (
      <SpanLines
        lines={[
          { span_id: 'span-A', color: 0, type: 'active' },
          { span_id: 'span-B', color: 1, type: 'active' },
          { span_id: 'span-C', color: 2, type: 'active' },
        ]}
        spanOpener={false}
      />
    ))
    const wrapper = container.firstElementChild
    expect(wrapper).toBeInTheDocument()
    expect(wrapper!.children.length).toBe(3)
  })

  it('bridges only continuing vertical span columns across the inter-row gap', () => {
    // The gap segments render in the SpanLineGapBridges overlay (outside the
    // paint-contained rows), not as in-row overhang. Column 0 ended in the
    // previous row (connector_end has no bottom continuation); column 1
    // continues, so exactly one bridge renders — at that column's center.
    const entries: SpanBridgeEntry[] = [
      {
        msg: { id: 'm1' },
        category: { kind: 'assistant_text' },
        parsedSpanLines: [
          { span_id: 'span-A', color: 1, type: 'connector_end' },
          { span_id: 'span-B', color: 2, type: 'active_passthrough', passthrough_color: 1 },
        ],
      },
      {
        msg: { id: 'm2' },
        category: { kind: 'assistant_text' },
        parsedSpanLines: [
          null,
          { span_id: 'span-B', color: 2, type: 'active' },
        ],
      },
    ]
    const { container } = render(() => (
      <SpanLineGapBridges
        entries={entries}
        precedingEntry={undefined}
        topOf={id => (id === 'm2' ? 240 : 120)}
        hiddenOf={() => false}
      />
    ))

    // m1 has no predecessor, so it earns no anchor; m2's column 1 continues.
    expect(container.querySelector('[data-span-gap-bridges-for="m1"]')).toBeNull()
    const anchor = container.querySelector('[data-span-gap-bridges-for="m2"]') as HTMLElement
    expect(anchor).toBeInTheDocument()
    expect(anchor.style.transform).toBe('translateY(240px)')
    const bridges = [...anchor.children] as HTMLElement[]
    expect(bridges).toHaveLength(1)
    expect(bridges[0].style.left).toBe(`${spanColumnCenterX(1) - LINE_THICKNESS / 2}px`)
  })

  it('draws one bridge per continuing column', () => {
    const lineA = { span_id: 'span-A', color: 1, type: 'active' } as const
    const lineB = { span_id: 'span-B', color: 2, type: 'active' } as const
    const entries: SpanBridgeEntry[] = [
      { msg: { id: 'm6' }, category: { kind: 'assistant_text' }, parsedSpanLines: [lineA, lineB] },
    ]
    const { container } = render(() => (
      <SpanLineGapBridges
        entries={entries}
        precedingEntry={{ msg: { id: 'm5' }, category: { kind: 'assistant_text' }, parsedSpanLines: [lineA, lineB] }}
        topOf={() => 300}
        hiddenOf={() => false}
      />
    ))
    const anchor = container.querySelector('[data-span-gap-bridges-for="m6"]') as HTMLElement
    const bridges = [...anchor.children] as HTMLElement[]
    expect(bridges).toHaveLength(2)
    expect(bridges[0].style.left).toBe(`${spanColumnCenterX(0) - LINE_THICKNESS / 2}px`)
    expect(bridges[1].style.left).toBe(`${spanColumnCenterX(1) - LINE_THICKNESS / 2}px`)
  })

  it('hides a bridge while its row is hidden-until-measured', () => {
    const line = { span_id: 'span-A', color: 1, type: 'active' } as const
    const entries: SpanBridgeEntry[] = [
      { msg: { id: 'm3' }, category: { kind: 'assistant_text' }, parsedSpanLines: [line] },
    ]
    const { container } = render(() => (
      <SpanLineGapBridges
        entries={entries}
        precedingEntry={{ msg: { id: 'm2' }, category: { kind: 'assistant_text' }, parsedSpanLines: [line] }}
        topOf={() => 100}
        hiddenOf={() => true}
      />
    ))
    const anchor = container.querySelector('[data-span-gap-bridges-for="m3"]') as HTMLElement
    expect(anchor.style.visibility).toBe('hidden')
    expect(anchor.children).toHaveLength(1)
  })

  it('connects via the preceding tool body key even when that row has no matching column', () => {
    const entries: SpanBridgeEntry[] = [
      {
        msg: { id: 'm4' },
        category: { kind: 'connector_end' },
        parsedSpanLines: [{ span_id: 'span-tool', color: 3, type: 'connector_end' }],
      },
    ]
    const { container } = render(() => (
      <SpanLineGapBridges
        entries={entries}
        precedingEntry={{
          // A tool_use body: its bottom border is the span's rail, so the
          // closing row below connects via the body key, not a column match.
          msg: { id: 'm3', spanId: 'span-tool', spanColor: 3 },
          category: { kind: 'tool_use' },
          parsedSpanLines: [],
        }}
        topOf={() => 60}
        hiddenOf={() => false}
      />
    ))
    const anchor = container.querySelector('[data-span-gap-bridges-for="m4"]') as HTMLElement
    expect(anchor.children).toHaveLength(1)
    expect((anchor.children[0] as HTMLElement).style.left).toBe(`${spanColumnCenterX(0) - LINE_THICKNESS / 2}px`)
  })

  it('treats connector_end as a top connector but not a bottom continuation', () => {
    expect(shouldConnectSpanLineTop(
      { span_id: 'span-A', color: 1, type: 'connector_end' },
      { span_id: 'span-A', color: 1, type: 'active' },
    )).toBe(true)

    expect(shouldConnectSpanLineTop(
      { span_id: 'span-A', color: 1, type: 'active' },
      { span_id: 'span-A', color: 1, type: 'connector_end' },
    )).toBe(false)
  })

  it('connects a closing row to the previous tool body border for the same span', () => {
    const previousToolBodyKey = bodySpanKey('span-tool', 3)

    expect(shouldConnectSpanLineTop(
      { span_id: 'span-tool', color: 3, type: 'connector_end' },
      { span_id: 'span-parent', color: 1, type: 'active' },
      previousToolBodyKey,
    )).toBe(true)

    expect(shouldConnectSpanLineTop(
      { span_id: 'span-other', color: 4, type: 'connector_end' },
      { span_id: 'span-parent', color: 1, type: 'active' },
      previousToolBodyKey,
    )).toBe(false)
  })
})
