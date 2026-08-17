/// <reference types="vitest/globals" />
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { popoverCard } from '~/styles/popover.css'
import { DropdownMenu, DropdownMenuCheckableItem, DropdownMenuItemContent } from './DropdownMenu'

// The jsdom popover stubs (showPopover/hidePopover/togglePopover plus the
// `:popover-open` matches interceptor) come from vitest.setup.ts, which runs
// before every test file.

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

  it('a JSX element trigger is wrapped in a div with display:contents', () => {
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

  /**
   * A card popover is a `div` AND the `popoverCard` class, and a call site that
   * applied one without the other is what produced both popover bugs this
   * component's `as` doc records. `card` supplies the class itself, so the pair
   * cannot come apart at a fifth call site.
   */
  it('renders as="card" as a div that carries popoverCard itself', () => {
    render(() => (
      <DropdownMenu trigger={<button>Open</button>} as="card" data-testid="card-popover">
        <span>rows</span>
      </DropdownMenu>
    ))
    const popover = screen.getByTestId('card-popover')
    expect(popover.tagName).toBe('DIV')
    expect(popover.className).toBe(popoverCard)
  })

  // ...and it takes the panel dismiss rule with it: a click on a row reads it or
  // selects its text, so the card must stay open.
  it('keeps an as="card" popover open on a content click', async () => {
    render(() => (
      <DropdownMenu trigger={<button>Open</button>} as="card" data-testid="card-popover">
        <span data-testid="card-row">a row</span>
      </DropdownMenu>
    ))
    await fireEvent.click(screen.getByText('Open'))
    const popover = screen.getByTestId('card-popover')
    const hide = vi.spyOn(popover, 'hidePopover')

    await fireEvent.click(screen.getByTestId('card-row'))

    expect(hide).not.toHaveBeenCalled()
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

  it('keeps the Escape dismiss handler on the as="div" popover (Dynamic swap)', () => {
    // The menu/div branches were collapsed into a single <Dynamic>; the dismiss
    // handlers must survive the swap on the non-default `div` tag, not just
    // `menu`. Escape dismisses BOTH tags -- only the click handler reads the tag
    // (see the content-click suite below).
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

    await fireEvent.click(screen.getByTestId('plain-trigger'))
    await fireEvent.click(screen.getByTestId('plain-item'))
    expect(hide).toHaveBeenCalled()
  })

  it('does not hide a second time when the item already closed the popover', async () => {
    // The item's own handler runs first. hidePopover() on a popover that is not
    // showing throws InvalidStateError in a browser, and the jsdom stub returns
    // early instead -- so the call COUNT is what this can assert. One call is
    // the item's own; a second would be the throw.
    let popoverEl: HTMLElement | undefined
    render(() => (
      <DropdownMenu
        id="self-closing-menu"
        data-testid="self-closing-popover"
        popoverRef={(el) => { popoverEl = el }}
        trigger={triggerProps => <button data-testid="self-closing-trigger" {...triggerProps}>Open</button>}
      >
        <button
          role="menuitem"
          data-testid="self-closing-item"
          onClick={() => popoverEl?.hidePopover()}
        >
          Item
        </button>
      </DropdownMenu>
    ))

    await fireEvent.click(screen.getByTestId('self-closing-trigger'))
    const hide = vi.spyOn(screen.getByTestId('self-closing-popover'), 'hidePopover')

    await fireEvent.click(screen.getByTestId('self-closing-item'))
    expect(hide).toHaveBeenCalledTimes(1)
  })
})

describe('dropdownMenu content-click dismiss', () => {
  it('does not dismiss a div popover when its content is clicked', async () => {
    // A `div` popover is a panel of content. Dismissing on a click would make
    // its text unselectable: the press starts the selection and the release
    // closes the popover under it.
    render(() => (
      <DropdownMenu
        as="div"
        id="card-popover-menu"
        data-testid="card-popover"
        trigger={triggerProps => <button data-testid="card-trigger" {...triggerProps}>Open</button>}
      >
        <span data-testid="card-text">Session ID</span>
        <button data-testid="card-copy">Copy</button>
      </DropdownMenu>
    ))

    const popover = screen.getByTestId('card-popover')
    const hide = vi.spyOn(popover, 'hidePopover')

    await fireEvent.click(screen.getByTestId('card-trigger'))
    await fireEvent.click(screen.getByTestId('card-text'))
    expect(hide).not.toHaveBeenCalled()

    // Not even a button inside it: a card's buttons act on the card (copy a
    // value), so they must leave it open too.
    await fireEvent.click(screen.getByTestId('card-copy'))
    expect(hide).not.toHaveBeenCalled()
  })

  it('dismisses a menu popover when its content is clicked', async () => {
    // The other half of the same rule: a `menu` popover is a list of commands,
    // so it still closes behind the click that runs one.
    render(() => (
      <DropdownMenu
        id="content-menu"
        data-testid="content-menu-popover"
        trigger={triggerProps => <button data-testid="content-menu-trigger" {...triggerProps}>Open</button>}
      >
        <button role="menuitem" data-testid="content-menu-item">Item</button>
      </DropdownMenu>
    ))

    const popover = screen.getByTestId('content-menu-popover')
    const hide = vi.spyOn(popover, 'hidePopover')

    await fireEvent.click(screen.getByTestId('content-menu-trigger'))
    await fireEvent.click(screen.getByTestId('content-menu-item'))
    expect(hide).toHaveBeenCalled()
  })
})

describe('dropdownMenu contextMenuFor', () => {
  /**
   * Render a row plus a menu whose `contextMenuFor` points at it. The row gets a
   * stubbed rect because jsdom does no layout and the press anchor is built from
   * the row's vertical band.
   */
  function renderRowMenu() {
    let rowEl!: HTMLDivElement

    render(() => (
      <>
        <div ref={rowEl} data-testid="row" onClick={() => {}}>row</div>
        <DropdownMenu
          id="row-menu"
          data-testid="row-menu-popover"
          contextMenuFor={() => rowEl}
          trigger={triggerProps => <button data-testid="row-kebab" {...triggerProps}>Open</button>}
        >
          <button role="menuitem" data-testid="row-menu-item">Rename</button>
        </DropdownMenu>
      </>
    ))

    const row = screen.getByTestId('row')
    row.getBoundingClientRect = () => ({
      top: 100,
      bottom: 122,
      left: 40,
      right: 240,
      width: 200,
      height: 22,
      x: 40,
      y: 100,
      toJSON: () => ({}),
    })

    const popover = screen.getByTestId('row-menu-popover')
    popover.getBoundingClientRect = () => ({
      top: 0,
      bottom: 150,
      left: 0,
      right: 200,
      width: 200,
      height: 150,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    })

    return { row, popover }
  }

  it('opens the menu on right-click', async () => {
    vi.useFakeTimers()
    try {
      const { row, popover } = renderRowMenu()
      const show = vi.spyOn(popover, 'showPopover')

      row.dispatchEvent(new MouseEvent('contextmenu', { clientX: 150, bubbles: true, cancelable: true }))
      vi.runAllTimers()

      expect(show).toHaveBeenCalled()
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('anchors the menu at the cursor, not at the row', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)
    try {
      const { row, popover } = renderRowMenu()

      row.dispatchEvent(new MouseEvent('contextmenu', { clientX: 150, clientY: 108, bubbles: true, cancelable: true }))
      vi.runAllTimers()

      expect(popover.style.left).toBe('150px') // the click x, not the row's left edge
      expect(popover.style.top).toBe('108px') // the click y, not the row's bottom
    }
    finally {
      vi.unstubAllGlobals()
      vi.useRealTimers()
    }
  })

  it('suppresses the native menu on the row', () => {
    const { row } = renderRowMenu()

    const e = new MouseEvent('contextmenu', { clientX: 150, bubbles: true, cancelable: true })
    row.dispatchEvent(e)

    expect(e.defaultPrevented).toBe(true)
  })

  it('hides a press-anchored menu when anything else scrolls', () => {
    vi.useFakeTimers()
    try {
      const { row, popover } = renderRowMenu()
      const hide = vi.spyOn(popover, 'hidePopover')

      row.dispatchEvent(new MouseEvent('contextmenu', { clientX: 150, bubbles: true, cancelable: true }))
      vi.runAllTimers()
      expect(popover.matches(':popover-open')).toBe(true)

      // A press anchor is a frozen point, not an element to follow: the row it
      // pointed at has scrolled away, so the menu closes instead of floating
      // over whatever took its place. Element-anchored menus keep repositioning.
      document.dispatchEvent(new Event('scroll'))

      expect(hide).toHaveBeenCalled()
      expect(popover.matches(':popover-open')).toBe(false)
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('attaches nothing when the accessor has no element yet', () => {
    // A row whose ref has not resolved must not throw or bind to anything.
    expect(() =>
      render(() => (
        <DropdownMenu id="empty-menu" contextMenuFor={() => undefined}>
          <button role="menuitem">Item</button>
        </DropdownMenu>
      )),
    ).not.toThrow()
  })

  /**
   * A trigger-less dropdown holds nothing but its `position: fixed` popover, and
   * `ot-dropdown` defaults to `display: inline`. Inside a flex row that empty
   * inline box is still a flex ITEM, adding one `gap` of dead space to every row
   * that mounts such a menu -- which is every tab row. The attribute is what the
   * `display: contents` rule in ~/styles/popover.css.ts keys on.
   */
  it('marks a trigger-less host headless so it takes no room in the row', () => {
    const { container } = render(() => (
      <DropdownMenu id="headless-menu" contextMenuFor={() => undefined}>
        <button role="menuitem">Item</button>
      </DropdownMenu>
    ))

    expect(container.querySelector('ot-dropdown')).toHaveAttribute('data-headless')
  })

  it('leaves a host with a trigger laid out as usual', () => {
    const { container } = render(() => (
      <DropdownMenu id="triggered-menu" trigger={<button>Open</button>}>
        <button role="menuitem">Item</button>
      </DropdownMenu>
    ))

    expect(container.querySelector('ot-dropdown')).not.toHaveAttribute('data-headless')
  })

  it('detaches on unmount', () => {
    vi.useFakeTimers()
    try {
      const { row, popover } = renderRowMenu()
      const show = vi.spyOn(popover, 'showPopover')

      cleanup()

      row.dispatchEvent(new MouseEvent('contextmenu', { clientX: 150, bubbles: true, cancelable: true }))
      vi.runAllTimers()

      expect(show).not.toHaveBeenCalled()
    }
    finally {
      vi.useRealTimers()
    }
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
