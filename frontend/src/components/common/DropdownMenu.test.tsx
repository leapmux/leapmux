import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { DropdownMenu } from './DropdownMenu'

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
  it('renders trigger and popover elements', () => {
    render(() => (
      <DropdownMenu
        trigger={<button data-testid="trigger">Open</button>}
        data-testid="popover"
      >
        <button role="menuitem">Item 1</button>
      </DropdownMenu>
    ))

    expect(screen.getByTestId('trigger')).toBeTruthy()
    expect(screen.getByTestId('popover')).toBeTruthy()
    expect(screen.getByText('Item 1')).toBeTruthy()
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
    expect(trigger.getAttribute('popovertarget')).toBeNull()
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
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
    expect(wrapper?.style.display).toBe('contents')
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

    expect(refEl).toBeTruthy()
    expect(refEl?.getAttribute('data-testid')).toBe('popover-ref-test')
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
    expect(popover).toBeTruthy()
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
    expect(popover.classList.contains('my-custom-class')).toBe(true)
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
    expect(btn).toBeTruthy()
    // Should be wrapped in a div with display:contents, same as JSX element path
    const wrapper = btn.parentElement
    expect(wrapper?.tagName).toBe('DIV')
    expect(wrapper?.style.display).toBe('contents')
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
    expect(btn.getAttribute('popovertarget')).toBeNull()
    expect(btn.parentElement?.style.display).not.toBe('contents')
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
    expect(popover).toBeTruthy()
    expect(screen.getByText('Content')).toBeTruthy()
  })
})
