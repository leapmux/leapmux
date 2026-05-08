import type { GridPopoverHost } from '~/components/shell/GridPopoverHost'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { onMount } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import {
  GridPopoverHostProvider,
  useGridPopover,
} from '~/components/shell/GridPopoverHost'

/**
 * Capture the host + anchor into the supplied callbacks so each test can
 * drive `host.open(...)` from outside the JSX tree. The `onMount` wrapper
 * keeps Solid's reactivity linter happy — capturing reactive references
 * during the component body is exactly what `onMount` is for.
 */
function HostHarness(props: {
  setHost: (host: GridPopoverHost | undefined) => void
  setAnchor: (el: HTMLButtonElement) => void
}) {
  const host = useGridPopover()
  onMount(() => props.setHost(host))
  return (
    <button ref={props.setAnchor} data-testid="anchor">
      anchor
    </button>
  )
}

interface Harness {
  host: GridPopoverHost | undefined
  anchor: HTMLButtonElement
}

function renderWithProvider(): Harness {
  let host: GridPopoverHost | undefined
  let anchor: HTMLButtonElement | null = null
  render(() => (
    <GridPopoverHostProvider>
      <HostHarness
        setHost={h => host = h}
        setAnchor={el => anchor = el}
      />
    </GridPopoverHostProvider>
  ))
  return { host, anchor: anchor! }
}

describe('gridPopoverHost', () => {
  it('renders the singleton popover only once regardless of consumer count', () => {
    render(() => (
      <GridPopoverHostProvider>
        <div data-testid="consumer-1" />
        <div data-testid="consumer-2" />
        <div data-testid="consumer-3" />
      </GridPopoverHostProvider>
    ))
    // Even with three consumers (representing three tiles), only one popover
    // exists in the DOM.
    expect(screen.getAllByTestId('grid-size-popover')).toHaveLength(1)
  })

  it('exposes an `open` host via context', () => {
    const { host } = renderWithProvider()
    expect(host).toBeDefined()
    expect(typeof host?.open).toBe('function')
  })

  it('host.open + cell click invokes the request\'s onSelect', () => {
    const onSelect = vi.fn()
    const { host, anchor } = renderWithProvider()
    host!.open({ anchor, onSelect })
    fireEvent.click(screen.getByTestId('grid-size-cell-1-2'))
    expect(onSelect).toHaveBeenCalledWith(2, 3)
  })

  it('a second open() replaces the pending request — only the latest onSelect fires', () => {
    const firstOnSelect = vi.fn()
    const secondOnSelect = vi.fn()
    const { host, anchor } = renderWithProvider()
    host!.open({ anchor, onSelect: firstOnSelect })
    host!.open({ anchor, onSelect: secondOnSelect })
    fireEvent.click(screen.getByTestId('grid-size-cell-0-0'))
    expect(firstOnSelect).not.toHaveBeenCalled()
    expect(secondOnSelect).toHaveBeenCalledWith(1, 1)
  })

  it('useGridPopover() outside a provider returns undefined (so callers can fall back)', () => {
    let host: GridPopoverHost | undefined
    render(() => (
      <HostHarness
        setHost={h => host = h}
        setAnchor={() => {}}
      />
    ))
    expect(host).toBeUndefined()
  })
})
