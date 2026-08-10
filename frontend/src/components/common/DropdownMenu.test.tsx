/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { DropdownMenu, DropdownMenuCheckableItem, DropdownMenuItemContent } from './DropdownMenu'

// jsdom does not implement the native Popover API.
// Stub the methods so the component can render without errors.
beforeAll(() => {
  if (!HTMLElement.prototype.showPopover) {
    HTMLElement.prototype.showPopover = vi.fn()
  }
  if (!HTMLElement.prototype.hidePopover) {
    HTMLElement.prototype.hidePopover = vi.fn()
  }
  if (!HTMLElement.prototype.togglePopover) {
    HTMLElement.prototype.togglePopover = vi.fn()
  }
})

describe('dropdownMenu', () => {
  it('renders trailing shortcut text in menu item content', () => {
    render(() => (
      <button role="menuitem">
        <DropdownMenuItemContent label="New agent..." shortcut="Ctrl+Shift+N" />
      </button>
    ))

    expect(screen.getByRole('menuitem', { name: /New agent\.\.\.Ctrl\+Shift\+N/ })).toBeInTheDocument()
    expect(screen.getByText('Ctrl+Shift+N')).toBeInTheDocument()
  })

  it('renders trigger and popover elements', () => {
    render(() => (
      <DropdownMenu
        trigger={<button data-testid="trigger">Open</button>}
        data-testid="popover"
      >
        <button role="menuitem">Item 1</button>
      </DropdownMenu>
    ))

    expect(screen.getByTestId('trigger')).toBeInTheDocument()
    expect(screen.getByTestId('popover')).toBeInTheDocument()
    expect(screen.getByText('Item 1')).toBeInTheDocument()
  })

  it('render-prop trigger receives aria-expanded and click handlers', () => {
    render(() => (
      <DropdownMenu
        id="test-menu"
        trigger={triggerProps => (
          <button
            data-testid="trigger"
            aria-expanded={triggerProps['aria-expanded']}
            ref={triggerProps.ref}
            onClick={triggerProps.onClick}
            onPointerDown={triggerProps.onPointerDown}
          >
            Open
          </button>
        )}
      >
        <button role="menuitem">Item 1</button>
      </DropdownMenu>
    ))

    const trigger = screen.getByTestId('trigger')
    // No popovertarget — toggling is handled via onClick + togglePopover()
    expect(trigger).not.toHaveAttribute('popovertarget')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
  })

  it('jSX element trigger is wrapped in a div with display:contents', () => {
    render(() => (
      <DropdownMenu
        id="wrap-test"
        trigger={<button data-testid="inner-btn">Open</button>}
      >
        <button role="menuitem">Item 1</button>
      </DropdownMenu>
    ))

    const innerBtn = screen.getByTestId('inner-btn')
    const wrapper = innerBtn.parentElement
    expect(wrapper?.tagName).toBe('DIV')
    expect(wrapper).toHaveStyle({ display: 'contents' })
  })

  it('popoverRef callback is called with the popover DOM element', () => {
    let refEl: HTMLElement | undefined

    render(() => (
      <DropdownMenu
        trigger={<button>Open</button>}
        popoverRef={(el) => { refEl = el }}
        data-testid="popover-ref-test"
      >
        <button role="menuitem">Item 1</button>
      </DropdownMenu>
    ))

    expect(refEl).toBeInTheDocument()
    expect(refEl).toHaveAttribute('data-testid', 'popover-ref-test')
  })

  it('renders as="div" instead of menu', () => {
    render(() => (
      <DropdownMenu
        trigger={<button>Open</button>}
        as="div"
        data-testid="div-popover"
      >
        <p>Info content</p>
      </DropdownMenu>
    ))

    const popover = screen.getByTestId('div-popover')
    expect(popover.tagName).toBe('DIV')
  })

  it('renders as menu by default', () => {
    render(() => (
      <DropdownMenu
        trigger={<button>Open</button>}
        data-testid="menu-popover"
      >
        <button role="menuitem">Item</button>
      </DropdownMenu>
    ))

    const popover = screen.getByTestId('menu-popover')
    expect(popover.tagName).toBe('MENU')
  })

  it('keeps the Escape + outside-click dismiss handlers on the as="div" popover (Dynamic swap)', () => {
    // The menu/div branches were collapsed into a single <Dynamic>; the dismiss
    // handlers must survive the swap on the non-default `div` tag, not just `menu`.
    const hide = vi.spyOn(HTMLElement.prototype, 'hidePopover')
    render(() => (
      <DropdownMenu trigger={<button>Open</button>} as="div" data-testid="dyn-div">
        <p>Info</p>
      </DropdownMenu>
    ))
    const popover = screen.getByTestId('dyn-div')
    expect(popover.tagName).toBe('DIV')

    popover.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(hide).toHaveBeenCalled()
    hide.mockClear()

    popover.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(hide).toHaveBeenCalled()
    hide.mockRestore()
  })

  it('custom id is applied to the popover', () => {
    render(() => (
      <DropdownMenu
        id="custom-id"
        trigger={<button>Open</button>}
      >
        <button role="menuitem">Item</button>
      </DropdownMenu>
    ))

    const popover = document.getElementById('custom-id')
    expect(popover).toBeInTheDocument()
    expect(popover?.tagName).toBe('MENU')
  })

  it('custom class is applied to the popover', () => {
    render(() => (
      <DropdownMenu
        trigger={<button>Open</button>}
        class="my-custom-class"
        data-testid="class-test"
      >
        <button role="menuitem">Item</button>
      </DropdownMenu>
    ))

    const popover = screen.getByTestId('class-test')
    expect(popover).toHaveClass('my-custom-class')
  })

  it('solid accessor trigger (zero-arg function) is resolved and wrapped like JSX element', () => {
    // Solid wraps component JSX (e.g. <IconButton />) in zero-arg accessor
    // functions. DropdownMenu must detect these via Function.length === 0,
    // call them to resolve the DOM node, and wrap in the display:contents div.
    const accessor = () => <button data-testid="accessor-btn">Icon</button>
    expect(accessor.length).toBe(0) // confirms it looks like an accessor

    render(() => (
      <DropdownMenu
        trigger={accessor}
        data-testid="accessor-popover"
      >
        <button role="menuitem">Item</button>
      </DropdownMenu>
    ))

    const btn = screen.getByTestId('accessor-btn')
    expect(btn).toBeInTheDocument()
    // Should be wrapped in a div with display:contents, same as JSX element path
    const wrapper = btn.parentElement
    expect(wrapper?.tagName).toBe('DIV')
    expect(wrapper).toHaveStyle({ display: 'contents' })
  })

  it('render-prop (function with parameter) is NOT treated as accessor', () => {
    // A render-prop has length >= 1 (declares a triggerProps parameter).
    // It must NOT be called as an accessor — it should receive triggerProps.
    render(() => (
      <DropdownMenu
        id="render-prop-test"
        trigger={triggerProps => (
          <button
            data-testid="rp-btn"
            ref={triggerProps.ref}
            onClick={triggerProps.onClick}
            onPointerDown={triggerProps.onPointerDown}
          >
            Open
          </button>
        )}
      >
        <button role="menuitem">Item</button>
      </DropdownMenu>
    ))

    const btn = screen.getByTestId('rp-btn')
    // Render-prop button uses onClick/onPointerDown — no display:contents wrapper
    expect(btn).not.toHaveAttribute('popovertarget')
    expect(btn.parentElement).not.toHaveStyle({ display: 'contents' })
  })

  it('renders without trigger when anchorRef is provided', () => {
    const anchor = document.createElement('div')

    render(() => (
      <DropdownMenu
        anchorRef={() => anchor}
        data-testid="no-trigger-popover"
      >
        <p>Content</p>
      </DropdownMenu>
    ))

    const popover = screen.getByTestId('no-trigger-popover')
    expect(popover).toBeInTheDocument()
    expect(screen.getByText('Content')).toBeInTheDocument()
  })
})

describe('dropdownMenu nested-submenu dismiss', () => {
  it('marks every trigger it renders, so an enclosing popover can recognize one', () => {
    render(() => (
      <DropdownMenu
        id="marks-trigger"
        trigger={triggerProps => <button data-testid="t" {...triggerProps}>Open</button>}
      >
        <button role="menuitem">Item</button>
      </DropdownMenu>
    ))

    expect(screen.getByTestId('t')).toHaveAttribute('data-dropdown-trigger')
  })

  it('does not dismiss itself when the click opens a nested submenu', async () => {
    // A nested dropdown's trigger is a DOM child of this popover, so without
    // the guard the parent hides on the very click that opens the submenu --
    // and hiding a popover hides its descendants with it.
    render(() => (
      <DropdownMenu
        id="parent-menu"
        data-testid="parent-popover"
        trigger={triggerProps => <button data-testid="parent-trigger" {...triggerProps}>Open</button>}
      >
        <button role="menuitem">Plain item</button>
        <DropdownMenu
          id="sub-menu"
          trigger={triggerProps => <button data-testid="sub-trigger" {...triggerProps}>More</button>}
        >
          <button role="menuitem">Nested item</button>
        </DropdownMenu>
      </DropdownMenu>
    ))

    const parent = screen.getByTestId('parent-popover')
    const hide = vi.spyOn(parent, 'hidePopover')

    await fireEvent.click(screen.getByTestId('sub-trigger'))
    expect(hide).not.toHaveBeenCalled()
  })

  it('still dismisses when the click activates one of its own items', async () => {
    render(() => (
      <DropdownMenu
        id="plain-menu"
        data-testid="plain-popover"
        trigger={triggerProps => <button data-testid="plain-trigger" {...triggerProps}>Open</button>}
      >
        <button role="menuitem" data-testid="plain-item">Item</button>
      </DropdownMenu>
    ))

    const popover = screen.getByTestId('plain-popover')
    const hide = vi.spyOn(popover, 'hidePopover')

    await fireEvent.click(screen.getByTestId('plain-item'))
    expect(hide).toHaveBeenCalled()
  })
})

describe('dropdownMenuCheckableItem', () => {
  it('renders a menuitemcheckbox with aria-checked and a checked indicator', () => {
    render(() => <DropdownMenuCheckableItem kind="checkbox" label="Show status bar" checked onSelect={() => {}} />)

    const item = screen.getByRole('menuitemcheckbox')
    expect(item).toHaveAttribute('aria-checked', 'true')
    const checkbox = screen.getByRole('checkbox', { hidden: true }) as HTMLInputElement
    expect(checkbox.checked).toBe(true)
    expect(checkbox).toBeDisabled()
    expect(checkbox).toHaveAttribute('aria-hidden', 'true')
    expect(screen.getByText('Show status bar')).toBeInTheDocument()
  })

  it('reports the unchecked state through aria-checked, not only the indicator', () => {
    render(() => <DropdownMenuCheckableItem kind="checkbox" label="Show status bar" checked={false} onSelect={() => {}} />)

    expect(screen.getByRole('menuitemcheckbox')).toHaveAttribute('aria-checked', 'false')
    expect((screen.getByRole('checkbox', { hidden: true }) as HTMLInputElement).checked).toBe(false)
  })

  it('renders a menuitemradio for kind="radio"', () => {
    render(() => <DropdownMenuCheckableItem kind="radio" label="Extra High" checked onSelect={() => {}} />)

    const item = screen.getByRole('menuitemradio')
    expect(item).toHaveAttribute('aria-checked', 'true')
    const radio = document.querySelector('input[type="radio"]') as HTMLInputElement
    expect(radio.checked).toBe(true)
    expect(radio).toBeDisabled()
    expect(screen.getByText('Extra High')).toBeInTheDocument()
  })

  it('calls onSelect when activated', () => {
    const onSelect = vi.fn()
    render(() => <DropdownMenuCheckableItem kind="checkbox" label="Toggle" checked={false} onSelect={onSelect} />)

    screen.getByRole('menuitemcheckbox').click()
    expect(onSelect).toHaveBeenCalledTimes(1)
  })

  it('does not call onSelect while disabled, and exposes the reason', () => {
    const onSelect = vi.fn()
    render(() => (
      <DropdownMenuCheckableItem
        kind="radio"
        label="Opus"
        checked={false}
        disabled
        title="This setting is controlled by the agent"
        onSelect={onSelect}
      />
    ))

    const item = screen.getByRole('menuitemradio')
    // Assert the MECHANISM, not the absence of a call. The native `disabled`
    // attribute is what stops activation: jsdom returns from `click()` on a
    // disabled element, and Solid's delegated click handler skips disabled
    // nodes too -- so `expect(onSelect).not.toHaveBeenCalled()` after
    // `item.click()` passes whether or not the component keeps a guard, and
    // catches nothing. The paired "calls onSelect when activated" test above is
    // what gives this one its meaning.
    expect(item).toBeDisabled()
    expect(item).toHaveAttribute('title', 'This setting is controlled by the agent')
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('keeps the indicator click-through so the whole row is one hit target', () => {
    render(() => <DropdownMenuCheckableItem kind="checkbox" label="Toggle" checked onSelect={() => {}} />)

    const checkbox = screen.getByRole('checkbox', { hidden: true }) as HTMLInputElement
    expect(checkbox.style.pointerEvents).toBe('none')
  })
})
