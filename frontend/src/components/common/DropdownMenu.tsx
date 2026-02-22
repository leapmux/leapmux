import type { Accessor, JSX } from 'solid-js'
import type { PopoverPositionOptions } from '~/lib/popoverPosition'
import { createEffect, createSignal, createUniqueId, on, onCleanup, Show } from 'solid-js'
import { calcPopoverPosition } from '~/lib/popoverPosition'

export interface DropdownTriggerProps {
  /** Whether the dropdown is currently open. */
  'aria-expanded': boolean
  /** Ref callback to capture the trigger element for positioning. */
  'ref': (el: HTMLElement) => void
  /** Pointerdown handler — must be spread onto the trigger element. */
  'onPointerDown': () => void
  /** Click handler — must be spread onto the trigger element. */
  'onClick': () => void
}

export interface DropdownMenuProps {
  /**
   * The trigger element. Two forms:
   * 1. Render function receiving trigger props (aria-expanded,
   *    ref, onPointerDown, onClick) to spread onto a native `<button>`:
   *    `trigger={(p) => <button {...p}>Open</button>}`
   * 2. JSX element or component (wrapped in a `<div>` with click handler):
   *    `trigger={<button>Open</button>}` or `trigger={<IconButton .../>}`
   *    Solid accessor functions (wrapping component JSX) are resolved
   *    automatically — callers don't need to unwrap them.
   *
   * Omit when using `anchorRef` + `open` for programmatic control.
   */
  'trigger'?: JSX.Element | ((props: DropdownTriggerProps) => JSX.Element)

  /**
   * Anchor element for positioning when no trigger is provided.
   * Used for programmatic popovers (e.g. CodeLanguagePopover).
   */
  'anchorRef'?: Accessor<HTMLElement | undefined>

  /**
   * Programmatic open/close control. When provided without a trigger,
   * the component calls showPopover()/hidePopover() reactively.
   */
  'open'?: Accessor<boolean>

  /** Popover content. */
  'children': JSX.Element

  /** Popover element tag: 'menu' (default) or 'div'. */
  'as'?: 'menu' | 'div'

  /** Positioning options. Default: { placement: 'auto' }. */
  'placement'?: PopoverPositionOptions

  /** Optional popover ID (auto-generated if omitted). */
  'id'?: string

  /** Ref callback for programmatic hidePopover(). */
  'popoverRef'?: (el: HTMLElement) => void

  /** CSS class on the popover element. */
  'class'?: string

  /** data-testid on the popover element. */
  'data-testid'?: string

  /** Callback when the popover opens or closes. */
  'onToggle'?: (open: boolean) => void
}

export function DropdownMenu(props: DropdownMenuProps) {
  // eslint-disable-next-line solid/reactivity -- read once for a stable element ID
  const menuId = props.id ?? createUniqueId()
  const [isOpen, setIsOpen] = createSignal(false)

  let triggerEl: HTMLElement | undefined
  let popoverEl: HTMLElement | undefined

  // Whether the popover was open when the current pointer interaction started.
  // Captured on pointerdown so that the click handler knows whether the user
  // intended to close (toggle off) vs open (toggle on) the popover.
  let wasOpenOnPointerDown = false

  const setTriggerRef = (el: HTMLElement) => {
    triggerEl = el
  }

  // Resolve the positioning anchor: for the JSX-element trigger path,
  // triggerEl is the display:contents wrapper whose bounding rect is zero.
  // Fall back to its first visible child element.
  const getAnchor = (): HTMLElement | undefined => {
    if (triggerEl) {
      const rect = triggerEl.getBoundingClientRect()
      if (rect.width === 0 && rect.height === 0 && triggerEl.firstElementChild) {
        return triggerEl.firstElementChild as HTMLElement
      }
      return triggerEl
    }
    return props.anchorRef?.()
  }

  const reposition = () => {
    const anchor = getAnchor()
    if (!anchor || !popoverEl)
      return

    const opts = props.placement ?? { placement: 'auto' }
    const { top, left, flipped } = calcPopoverPosition(anchor, popoverEl, opts)
    popoverEl.style.top = `${top}px`
    popoverEl.style.left = `${left}px`

    if (flipped) {
      popoverEl.setAttribute('data-flipped', '')
    }
    else {
      popoverEl.removeAttribute('data-flipped')
    }
  }

  // Reposition only when the scroll originates outside the popover itself.
  // This prevents the popover from jumping when the user selects text
  // inside the popover content by dragging.
  const repositionOnExternalScroll = (e: Event) => {
    if (popoverEl && e.target instanceof Node && popoverEl.contains(e.target)) {
      return
    }
    reposition()
  }

  const handleToggle = (e: Event) => {
    const toggleEvent = e as ToggleEvent
    const opening = toggleEvent.newState === 'open'
    setIsOpen(opening)

    if (opening) {
      // Reposition after OAT's own positioning
      requestAnimationFrame(() => {
        reposition()
      })
      window.addEventListener('scroll', repositionOnExternalScroll, true)

      const anchor = getAnchor()
      if (anchor) {
        anchor.setAttribute('aria-expanded', 'true')
      }
    }
    else {
      window.removeEventListener('scroll', repositionOnExternalScroll, true)

      const anchor = getAnchor()
      if (anchor) {
        anchor.setAttribute('aria-expanded', 'false')
      }
    }

    props.onToggle?.(opening)
  }

  const popoverRefCallback = (el: HTMLElement) => {
    popoverEl = el
    props.popoverRef?.(el)

    // Use native addEventListener for the toggle event to avoid any
    // framework-level event handling differences.
    el.addEventListener('toggle', handleToggle)
  }

  // Programmatic open/close when `open` accessor is provided
  // eslint-disable-next-line solid/reactivity -- guards presence of accessor; on() tracks it reactively
  if (props.open) {
    createEffect(on(props.open, (shouldOpen) => { // eslint-disable-line solid/reactivity -- passing accessor to on() is correct
      if (!popoverEl)
        return
      if (shouldOpen) {
        popoverEl.showPopover()
      }
      else {
        popoverEl.hidePopover()
      }
    }))
  }

  onCleanup(() => {
    popoverEl?.removeEventListener('toggle', handleToggle)
    window.removeEventListener('scroll', repositionOnExternalScroll, true)
  })

  const handleTriggerPointerDown = () => {
    // Capture the popover's open state before light-dismiss has a chance
    // to close it. The browser records the pointerdown target and runs
    // the light-dismiss algorithm just before dispatching the click event.
    wasOpenOnPointerDown = isOpen()
  }

  const handleTriggerClick = () => {
    // If the popover was open when the pointer went down, light-dismiss
    // already closed it — don't reopen it with togglePopover().
    if (wasOpenOnPointerDown) {
      return
    }
    popoverEl?.togglePopover()
  }

  const triggerProps: DropdownTriggerProps = {
    get 'aria-expanded'() { return isOpen() },
    'ref': setTriggerRef,
    'onPointerDown': handleTriggerPointerDown,
    'onClick': handleTriggerClick,
  }

  // Determine whether the trigger is a render-prop function (takes
  // DropdownTriggerProps) or a Solid accessor / JSX element. Render-props
  // have length >= 1 (they declare a parameter), while Solid accessors
  // wrapping component JSX are zero-arg thunks.
  const isRenderProp = () => {
    const t = props.trigger
    return typeof t === 'function' && t.length > 0
  }

  const renderTrigger = () => {
    if (isRenderProp()) {
      return (props.trigger as (p: DropdownTriggerProps) => JSX.Element)(triggerProps)
    }
    // Resolve the trigger value: if it's a Solid accessor (zero-arg
    // function wrapping component JSX), call it to get the DOM node.
    const resolved = typeof props.trigger === 'function'
      ? (props.trigger as () => JSX.Element)()
      : props.trigger
    if (resolved != null) {
      // Wrap JSX element trigger in a <div> with a click handler.
      // We use onClick + togglePopover() instead of popovertarget because
      // popovertarget only works on <button> and <input> elements.
      return (
        <div
          ref={setTriggerRef}
          onPointerDown={handleTriggerPointerDown}
          onClick={handleTriggerClick}
          style={{ display: 'contents' }}
        >
          {resolved}
        </div>
      )
    }
    return null
  }

  return (
    <ot-dropdown>
      {renderTrigger()}
      <Show
        when={props.as === 'div'}
        fallback={(
          <menu
            popover="auto"
            id={menuId}
            ref={popoverRefCallback /* eslint-disable-line solid/reactivity -- ref callback, not a signal */}
            class={props.class}
            data-testid={props['data-testid']}
          >
            {props.children}
          </menu>
        )}
      >
        <div
          popover="auto"
          id={menuId}
          ref={popoverRefCallback /* eslint-disable-line solid/reactivity -- ref callback, not a signal */}
          class={props.class}
          data-testid={props['data-testid']}
        >
          {props.children}
        </div>
      </Show>
    </ot-dropdown>
  )
}
