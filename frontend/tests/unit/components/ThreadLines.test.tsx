import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ThreadLines } from '~/components/chat/ThreadLines'

describe('threadLines', () => {
  it('renders nothing when lines array is empty', () => {
    const { container } = render(() => (
      <ThreadLines lines={[]} scopeId="" />
    ))
    // The <Show> guard prevents rendering when lines is empty.
    expect(container.children.length).toBe(0)
  })

  it('renders a container with one column per line entry', () => {
    const { container } = render(() => (
      <ThreadLines
        lines={[{ scope_id: 'scope-A', color: 0 }]}
        scopeId="scope-B"
      />
    ))
    // Should render a container div with one child (the column).
    const wrapper = container.firstElementChild
    expect(wrapper).not.toBeNull()
    expect(wrapper!.children.length).toBe(1)
  })

  it('renders correct number of columns with null entries', () => {
    const { container } = render(() => (
      <ThreadLines
        lines={[{ scope_id: 'scope-A', color: 0 }, null, { scope_id: 'scope-B', color: 1 }]}
        scopeId="scope-A"
      />
    ))
    const wrapper = container.firstElementChild
    expect(wrapper).not.toBeNull()
    // 3 columns: connected, empty, active
    expect(wrapper!.children.length).toBe(3)
  })

  it('renders columns for parallel scopes', () => {
    const { container } = render(() => (
      <ThreadLines
        lines={[
          { scope_id: 'scope-A', color: 0 },
          { scope_id: 'scope-B', color: 1 },
          { scope_id: 'scope-C', color: 2 },
        ]}
        scopeId=""
      />
    ))
    const wrapper = container.firstElementChild
    expect(wrapper).not.toBeNull()
    expect(wrapper!.children.length).toBe(3)
  })
})
