import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { bodySpanKey, shouldConnectSpanLineTop, SpanLines } from '~/components/chat/widgets/SpanLines'
import { ROW_GAP } from '~/components/chat/widgets/SpanLines.geometry'

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

  it('connects only continuing vertical span columns to the previous row', () => {
    const { container } = render(() => (
      <SpanLines
        previousLines={[
          { span_id: 'span-A', color: 1, type: 'connector_end' },
          { span_id: 'span-B', color: 2, type: 'active_passthrough', passthrough_color: 1 },
        ]}
        lines={[
          null,
          { span_id: 'span-B', color: 2, type: 'active' },
        ]}
      />
    ))

    const wrapper = container.firstElementChild
    expect(wrapper).toBeInTheDocument()
    const columns = [...wrapper!.children] as HTMLElement[]
    expect(columns[0]!.style.getPropertyValue('--span-row-top-overhang')).toBe('0px')
    expect(columns[1]!.style.getPropertyValue('--span-row-top-overhang')).toBe(ROW_GAP)
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
