import type { Accessor, JSX } from 'solid-js'
import type { PopoverPositionOptions } from '~/lib/popoverPosition'
import { createEffect, createSignal, createUniqueId, on, onCleanup, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { calcPopoverPosition } from '~/lib/popoverPosition'
import { menuItemContent, menuItemLabel, menuItemShortcut } from '~/styles/shared.css'

/**
 * Marks a dropdown's trigger element. An enclosing popover's dismiss handler
 * reads it to tell "the user opened a submenu" from "the user activated an
 * item", which are otherwise the same click on one of its own descendants.
 */
const TRIGGER_ATTR = 'data-dropdown-trigger'

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

  /**
   * Accessible name for the popover element.
   *
   * A `<menu>` of `menuitemradio` items carries no name of its own, so assistive
   * technology announces the options with nothing that says which axis they
   * belong to. Set this whenever the items are one named group.
   */
  'aria-label'?: string

  /**
   * Whether a click on the popover's own content dismisses it. Default true.
   *
   * True is the MENU contract: every click inside is an item activation, so the
   * menu closes behind it. Set false for a popover whose content is a FORM —
   * a field, a Save button, a destructive action — where a click is part of
   * filling the thing in, and dismissing on it makes the form unusable.
   */
  'closeOnContentClick'?: boolean

  /** Callback when the popover opens or closes. */
  'onToggle'?: (open: boolean) => void
}

export interface DropdownMenuItemContentProps {
  label: JSX.Element
  shortcut?: string
}

export function DropdownMenuItemContent(props: DropdownMenuItemContentProps) {
  return (
    <span class={menuItemContent}>
      <span class={menuItemLabel}>{props.label}</span>
      <Show when={props.shortcut}>
        {shortcut => <span class={menuItemShortcut}>{shortcut()}</span>}
      </Show>
    </span>
  )
}

export interface DropdownMenuCheckableItemProps {
  /** `checkbox` for an independent toggle, `radio` for one choice of a set. */
  'kind': 'checkbox' | 'radio'
  'label': string
  'checked': boolean
  'disabled'?: boolean
  /** Hover text, typically the reason the item is disabled. */
  'title'?: string
  'data-testid'?: string
  /** Invoked on activation. Not called while `disabled`. */
  'onSelect': () => void
}

/**
 * A menu item carrying a checked state: an OAT checkbox or radio showing that
 * state, followed by the label.
 *
 * The component renders the whole item — the button, its ARIA role, and
 * `aria-checked` — rather than the indicator alone. The indicator is
 * display-only (`disabled`, `aria-hidden`, no `onChange`) and therefore invisible
 * to assistive technology, so a caller that supplied its own `role="menuitem"`
 * button announced the item with no on/off state at all. Owning the button makes
 * that mistake impossible.
 */
export function DropdownMenuCheckableItem(props: DropdownMenuCheckableItemProps) {
  return (
    <button
      role={props.kind === 'checkbox' ? 'menuitemcheckbox' : 'menuitemradio'}
      aria-checked={props.checked}
      disabled={props.disabled}
      title={props.title}
      data-testid={props['data-testid']}
      // The guard is unreachable while the button carries the native `disabled`
      // attribute above -- no engine dispatches a click on a disabled element,
      // and Solid's delegated handler skips disabled nodes as well. It stays
      // because it becomes load-bearing the moment this item moves to
      // `aria-disabled` (which menus do, to keep a disabled item focusable), and
      // a silent switch from "does nothing" to "dispatches a settings change"
      // is the failure it prevents. No test can cover it; see DropdownMenu.test.
      onClick={() => {
        if (!props.disabled)
          props.onSelect()
      }}
    >
      <span class={menuItemContent}>
        <input type={props.kind} checked={props.checked} disabled aria-hidden="true" style={{ 'pointer-events': 'none' }} />
        <span class={menuItemLabel}>{props.label}</span>
      </span>
    </button>
  )
}

export function DropdownMenu(props: DropdownMenuProps) {
  // eslint-disable-next-line solid/reactivity -- read once for a stable element ID
  const menuId = props.id ?? createUniqueId()
  const [isOpen, setIsOpen] = createSignal(false)

  let triggerEl: HTMLElement | undefined
  let popoverEl: HTMLElement | undefined
  // Reposition when the popover's content grows/shrinks while open. The initial
  // reposition runs in one rAF, but content that populates or relayouts after
  // that (e.g. a long, slowly-rendered language list, or the list shrinking as
  // the filter narrows it) would otherwise leave the popover mispositioned --
  // measured at the wrong height, so anchored/clamped against a stale size.
  let resizeObserver: ResizeObserver | undefined

  // Whether the popover was open when the current pointer interaction started.
  // Captured on pointerdown so that the click handler knows whether the user
  // intended to close (toggle off) vs open (toggle on) the popover.
  let wasOpenOnPointerDown = false

  const setTriggerRef = (el: HTMLElement) => {
    triggerEl = el
    // Mark the trigger so an ancestor popover's dismiss handler can recognize
    // it. A nested dropdown's trigger is a DOM child of the parent popover, so
    // without this the parent hides itself on the very click that opens the
    // submenu — and hiding a popover hides its descendants too.
    el.setAttribute(TRIGGER_ATTR, '')
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

  const handleToggle = (_e: Event) => {
    // Read the post-transition state from the popover element rather than
    // ToggleEvent.newState. Spec-wise both are equivalent (the toggle event
    // fires after the state change), but the jsdom popover stub dispatches
    // a plain Event without the ToggleEvent shape — checking `:popover-open`
    // works in both environments.
    const opening = popoverEl?.matches(':popover-open') ?? false
    setIsOpen(opening)

    if (opening) {
      // Reposition after OAT's own positioning
      requestAnimationFrame(() => {
        reposition()
      })
      window.addEventListener('scroll', repositionOnExternalScroll, true)

      // Re-anchor whenever the content's measured size settles after the initial
      // rAF (a large list paints over multiple frames; the filter shrinks it).
      // ResizeObserver fires once on observe() with the current size, then on
      // every change -- reposition() only moves the popover, never resizes it,
      // so this can't loop.
      if (typeof ResizeObserver !== 'undefined' && popoverEl) {
        resizeObserver?.disconnect()
        resizeObserver = new ResizeObserver(() => reposition())
        resizeObserver.observe(popoverEl)
      }

      const anchor = getAnchor()
      if (anchor) {
        anchor.setAttribute('aria-expanded', 'true')
      }
    }
    else {
      window.removeEventListener('scroll', repositionOnExternalScroll, true)
      resizeObserver?.disconnect()
      resizeObserver = undefined

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
      // Guard against redundant show/hide: showPopover() on an already-open popover
      // (or hidePopover() on a closed one) throws InvalidStateError, which would
      // abort this effect and desync the `open` signal from the native popover
      // state (re-clicks then reopen instead of closing). Read the native state
      // directly (`:popover-open`) rather than the `isOpen()` signal mirror, which a
      // dialog-driven auto-dismiss can leave stale.
      const nativeOpen = popoverEl.matches(':popover-open')
      if (shouldOpen && !nativeOpen) {
        popoverEl.showPopover()
        // Position synchronously, before the browser paints this frame, so the
        // popover never appears at the UA-default position and then jumps. The
        // content is already in the DOM (rendered before this effect), so it
        // measures at its real size here. The rAF reposition in handleToggle + the
        // ResizeObserver then refine it for any late layout.
        reposition()
      }
      else if (!shouldOpen && nativeOpen) {
        popoverEl.hidePopover()
      }
    }))
  }

  onCleanup(() => {
    popoverEl?.removeEventListener('toggle', handleToggle)
    window.removeEventListener('scroll', repositionOnExternalScroll, true)
    resizeObserver?.disconnect()
  })

  const handleTriggerPointerDown = () => {
    // Capture the popover's open state before light-dismiss has a chance
    // to close it. The browser records the pointerdown target and runs
    // the light-dismiss algorithm just before dispatching the click event.
    //
    // Read the actual DOM state via :popover-open instead of the isOpen
    // signal, because showModal() on a <dialog> auto-dismisses
    // popover="auto" elements WITHOUT firing a toggle event, leaving
    // isOpen stale.
    const actuallyOpen = popoverEl?.matches(':popover-open') ?? false
    if (isOpen() !== actuallyOpen) {
      setIsOpen(actuallyOpen)
    }
    wasOpenOnPointerDown = actuallyOpen
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
      {/* `menu` (default) and `div` popovers differ ONLY by tag; everything else (the
          popover attr, id, ref, class, testid, and the Escape/outside-click dismiss
          handlers) is identical, so render via Dynamic instead of two byte-identical
          branches that could drift. */}
      <Dynamic
        component={props.as === 'div' ? 'div' : 'menu'}
        popover="auto"
        id={menuId}
        ref={popoverRefCallback}
        class={props.class}
        data-testid={props['data-testid']}
        aria-label={props['aria-label']}
        onKeyDown={(e: KeyboardEvent) => {
          if (e.key === 'Escape') {
            e.preventDefault()
            popoverEl?.hidePopover()
          }
        }}
        onClick={(e: MouseEvent) => {
          e.stopPropagation()
          // A form's content is not a set of activations, so it opts out
          // entirely (see `closeOnContentClick`).
          if (props.closeOnContentClick === false)
            return
          // A click on a nested dropdown's trigger opens that submenu; it must
          // not also dismiss this popover, which would close the submenu with
          // it. Every other click inside the popover is an activation, so it
          // closes the menu as usual.
          if ((e.target as Element | null)?.closest?.(`[${TRIGGER_ATTR}]`))
            return
          popoverEl?.hidePopover()
        }}
      >
        {props.children}
      </Dynamic>
    </ot-dropdown>
  )
}
