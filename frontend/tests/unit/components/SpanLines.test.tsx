import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { SpanLines } from '~/components/chat/SpanLines'

describe('spanLines', () => {
  it('renders nothing when lines array is empty', () => {
    const { container } = render(() => (
      <SpanLines lines={[]} />
    ))
    // The <Show> guard prevents rendering when lines is empty.
    expect(container.children.length).toBe(0)
  })

  it('renders a container with one column per line entry', () => {
    const { container } = render(() => (
      <SpanLines
        lines={[{ span_id: 'span-A', color: 0 }]}
        spanOpener
      />
    ))
    // Should render a container div with one child (the column).
    const wrapper = container.firstElementChild
    expect(wrapper).not.toBeNull()
    expect(wrapper!.children.length).toBe(1)
  })

  it('renders correct number of columns with null entries', () => {
    const { container } = render(() => (
      <SpanLines
        lines={[{ span_id: 'span-A', color: 0 }, null, { span_id: 'span-B', color: 1 }]}
        spanOpener
      />
    ))
    const wrapper = container.firstElementChild
    expect(wrapper).not.toBeNull()
    // 3 columns: connected, empty, active
    expect(wrapper!.children.length).toBe(3)
  })

  it('renders columns for parallel spans', () => {
    const { container } = render(() => (
      <SpanLines
        lines={[
          { span_id: 'span-A', color: 0 },
          { span_id: 'span-B', color: 1 },
          { span_id: 'span-C', color: 2 },
        ]}
        spanOpener={false}
      />
    ))
    const wrapper = container.firstElementChild
    expect(wrapper).not.toBeNull()
    expect(wrapper!.children.length).toBe(3)
  })
})
