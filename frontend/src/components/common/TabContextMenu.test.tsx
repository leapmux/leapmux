import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { TabContextMenu } from './TabContextMenu'

/** Records what the mocked DropdownMenu last received for `contextMenuFor`. */
const captured: { contextMenuFor?: () => HTMLElement | undefined } = {}

// Mock DropdownMenu to render children directly (jsdom lacks the popover API).
vi.mock('~/components/common/DropdownMenu', () => ({
  DropdownMenu(props: any) {
    // eslint-disable-next-line solid/reactivity -- capturing the accessor itself for the assertion, not reading it
    captured.contextMenuFor = props.contextMenuFor
    return <>{props.children}</>
  },
}))

describe('tabContextMenu', () => {
  it('renders nothing when the tab offers no action', () => {
    const { container } = render(() => <TabContextMenu />)

    expect(container.textContent).toBe('')
    expect(screen.queryByRole('menuitem')).not.toBeInTheDocument()
  })

  it('offers rename and close for an ordinary tab', () => {
    render(() => <TabContextMenu onRename={() => {}} onClose={() => {}} />)

    expect(screen.getByTestId('tab-menu-rename')).toBeInTheDocument()
    expect(screen.getByTestId('tab-menu-close')).toBeInTheDocument()
  })

  it('hides rename for a tab that cannot be renamed', () => {
    render(() => <TabContextMenu onClose={() => {}} />)

    expect(screen.queryByTestId('tab-menu-rename')).not.toBeInTheDocument()
    expect(screen.getByTestId('tab-menu-close')).toBeInTheDocument()
  })

  it('hides close for a tab that cannot be closed', () => {
    render(() => <TabContextMenu onRename={() => {}} />)

    expect(screen.getByTestId('tab-menu-rename')).toBeInTheDocument()
    expect(screen.queryByTestId('tab-menu-close')).not.toBeInTheDocument()
  })

  it('shows the pop affordance with the label the surface supplied', () => {
    const onClick = vi.fn()
    render(() => (
      <TabContextMenu
        onClose={() => {}}
        pop={{ label: 'Pop out to floating window', onClick }}
      />
    ))

    const item = screen.getByTestId('tab-menu-pop')
    expect(item).toHaveTextContent('Pop out to floating window')

    fireEvent.click(item)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('omits the pop affordance when the surface has none', () => {
    render(() => <TabContextMenu onRename={() => {}} onClose={() => {}} />)

    expect(screen.queryByTestId('tab-menu-pop')).not.toBeInTheDocument()
  })

  it('runs rename and close exactly once each', () => {
    const onRename = vi.fn()
    const onClose = vi.fn()
    render(() => <TabContextMenu onRename={onRename} onClose={onClose} />)

    fireEvent.click(screen.getByTestId('tab-menu-rename'))
    fireEvent.click(screen.getByTestId('tab-menu-close'))

    expect(onRename).toHaveBeenCalledTimes(1)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('keeps close visible but inert while a close is already in flight', () => {
    const onClose = vi.fn()
    render(() => <TabContextMenu onRename={() => {}} onClose={onClose} isClosing />)

    const item = screen.getByTestId('tab-menu-close')
    // Visible, so the menu does not reshape under the user mid-click.
    expect(item).toBeInTheDocument()
    // The native `disabled` attribute is the mechanism; the guard in the handler
    // is what keeps it correct if this ever moves to `aria-disabled`.
    expect(item).toBeDisabled()

    fireEvent.click(item)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('separates close from the items above it, and only when there are any', () => {
    const { container: withRename } = render(() => (
      <TabContextMenu onRename={() => {}} onClose={() => {}} />
    ))
    expect(withRename.querySelector('hr')).toBeInTheDocument()

    const { container: closeOnly } = render(() => <TabContextMenu onClose={() => {}} />)
    expect(closeOnly.querySelector('hr')).not.toBeInTheDocument()
  })

  it('forwards contextMenuFor so the row itself opens the menu', () => {
    const row = document.createElement('div')

    render(() => <TabContextMenu contextMenuFor={() => row} onClose={() => {}} />)

    expect(captured.contextMenuFor?.()).toBe(row)
  })
})
