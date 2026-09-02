import type { Accessor, JSX, JSXElement } from 'solid-js'
import type { ContextMenuPress } from './contextMenuGesture'
import type { PopoverAnchor, PopoverPositionOptions } from '~/lib/popoverPosition'
import { createEffect, createSignal, createUniqueId, on, onCleanup, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { calcPopoverPosition } from '~/lib/popoverPosition'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { DIALOG_HEIGHT_VAR, popoverCard, popoverFieldMenuClamp, popoverMenuClamp, TRIGGER_WIDTH_VAR } from '~/styles/popover.css'
import { clippedText, menuItemContent, menuItemDetail } from '~/styles/shared.css'
import { ClippedText } from './ClippedText'
import { attachContextMenuGesture, holdIsOverMenu, pressAnchorRect } from './contextMenuGesture'
import { dismissActiveTooltip } from './Tooltip'

/**
 * Marks a dropdown's trigger element. An enclosing popover's dismiss handler
 * reads it to tell "the user opened a submenu" from "the user activated an
 * item", which are otherwise the same click on one of its own descendants.
 */
const TRIGGER_ATTR = 'data-dropdown-trigger'

/**
 * Every focusable command inside a menu popover.
 *
 * The three menu item roles this component renders, plus a plain `<button>` for
 * the plain `DropdownMenuItem`. A disabled item is EXCLUDED: this menu disables
 * with the native attribute, so a disabled item is not focusable and arrowing
 * onto it would strand the focus.
 */
const MENU_ITEM_SELECTOR = [
  '[role="menuitem"]:not(:disabled)',
  '[role="menuitemradio"]:not(:disabled)',
  '[role="menuitemcheckbox"]:not(:disabled)',
  'button:not(:disabled):not([role])',
].join(',')

/**
 * How long a type-ahead buffer survives without a keystroke.
 *
 * The native `<select>` uses roughly this, and the shape matters more than the
 * exact number: typing `g`,`i`,`t` quickly means "git", and typing `g` twice
 * slowly means "the next thing starting with g".
 */
const TYPE_AHEAD_RESET_MS = 700

/**
 * Show the menu, as `manual` when a long press still holds a finger over it
 * and as the plain `auto` popover otherwise.
 *
 * A menu that opens under a pressing finger cannot be an `auto` popover: the
 * release still to come is what the HTML light-dismiss pass acts on, and it
 * hides every `auto` popover whose chain excludes that release. Which engine
 * hides what, and when, is not something to code around -- Chromium hides
 * before dispatching the release, WebKit after, and WebKit hit-tests the
 * release live, so whether the menu survived depended on where the finger
 * happened to be. `manual` is not light-dismissed at all, and
 * `armPressDismiss` replaces the one behavior that matters: it closes the menu
 * on a press outside. `Escape` needs nothing: `handleMenuKeyDown` calls
 * `hidePopover` itself.
 *
 * Asked at SHOW TIME, of the gesture rather than of the caller, because both
 * ways this component opens need it: the gesture it attaches itself, and a
 * controlled `open` driven by a singleton host -- which is how the chat list's
 * shared message menu opens, and why a fix to the first path alone left the
 * menu that a phone user actually presses to vanish on release.
 *
 * @returns whether the menu opened in manual mode, so the caller can arm the
 * dismissal that goes with it.
 */
let swappingHoldMode = false

function showMenuPopover(popover: HTMLElement): boolean {
  dismissActiveTooltip()
  const manual = holdIsOverMenu()
  // The attribute can only be swapped while the popover is hidden -- changing
  // it on a showing one hides it -- so it brackets the show here and the close
  // in `handleToggle`. Every other way of opening this menu keeps `auto`.
  if (manual) {
    if (popover.matches(':popover-open') && popover.getAttribute('popover') !== 'manual') {
      // jsdom (and some engines) dispatch `toggle` synchronously from
      // hidePopover. That close must not wipe the press we are about to
      // re-open under, or the kebab's element-anchor wins the next paint.
      swappingHoldMode = true
      try {
        popover.hidePopover()
      }
      finally {
        swappingHoldMode = false
      }
    }
    popover.setAttribute('popover', 'manual')
    popover.setAttribute('data-ctx-hold-inert', '')
  }
  popover.showPopover()
  return manual
}

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

/**
 * The row-menu half of `DropdownMenuProps`, for the thin wrappers that own one
 * row's menu (`WorkspaceContextMenu`, `BranchContextMenu`, `WorkerContextMenu`,
 * `TunnelContextMenu`, `FileActionsMenu`, `TabContextMenu`). Each extends this
 * instead of redeclaring the prop, so the contract and its documentation stay in
 * one place.
 */
export interface ContextMenuTargetProps {
  /** See `DropdownMenuProps.contextMenuFor`. */
  contextMenuFor?: Accessor<HTMLElement | undefined>
}

/**
 * The row element for a `contextMenuFor` menu, as a signal so the menu's attach
 * effect tracks it -- a plain `let` depends on an assignment to the ref before
 * effects flush. Pass the accessor to `contextMenuFor`, and use the setter
 * as the row's `ref`, or call it first inside a composed ref callback beside the
 * row's own directives (`sortable`, `draggable`, an imperative `let` for
 * scroll-into-view, ...).
 */
export function createContextMenuAnchor(): [Accessor<HTMLElement | undefined>, (el: HTMLElement) => void] {
  const [anchor, setAnchor] = createSignal<HTMLElement>()
  return [anchor, setAnchor]
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
   * Anchor for positioning when the caller gives no trigger.
   * Used for programmatic popovers (e.g. CodeLanguagePopover).
   */
  'anchorRef'?: Accessor<PopoverAnchor | undefined>

  /**
   * Programmatic open/close control. When provided without a trigger,
   * the component calls showPopover()/hidePopover() reactively.
   */
  'open'?: Accessor<boolean>

  /**
   * The element whose RIGHT-CLICK or touch LONG-PRESS opens this menu -- normally
   * the row the menu belongs to. The menu then opens at the press point rather
   * than at `trigger`; see `pressAnchorRect`.
   *
   * The kebab `trigger` keeps working unchanged. This is a second way in, for the
   * two inputs that have no hover: a mouse's secondary button, and a finger.
   *
   * Rows whose text must stay selectable attach the gesture directly with
   * `selectableText` instead (see ~/components/common/contextMenuGesture.ts) --
   * the chat message rows, whose menu is a singleton host and not this component.
   */
  'contextMenuFor'?: Accessor<HTMLElement | undefined>

  /** Popover content. */
  'children': JSX.Element

  /**
   * What the popover IS. This sets both its element and its dismiss rule:
   *
   * - `menu` (default) renders a `<menu>` of commands. Every click inside it
   *   activates an item, so the popover closes behind the click.
   * - `div` renders a `<div>` of content. A click inside it reads a row, selects
   *   text, or fills in a control, so the popover STAYS OPEN. Content that is a
   *   real action closes the popover from its own handler -- through the element
   *   `popoverRef` captured, or through the `open` accessor -- because only that
   *   handler knows the action ran.
   * - `card` is a `div` whose content is a CARD -- labelled rows, a list, a
   *   panel. It supplies the `popoverCard` class ITSELF, so a call site cannot
   *   apply the element without the class or the class without the element.
   *   That pair is exactly what drifted apart before: the `[+]` menu's agent-info
   *   card carried the class but stayed a `<menu>`, so it dismissed on every
   *   click, and the chip popovers carried `as="div"` with a bare `card`, so
   *   they had no positioning reset and no viewport clamp.
   *
   * The element and the dismiss rule are one decision, so they are one prop: a
   * popover that closes on every click cannot hold selectable text, and a
   * popover that never closes cannot be a menu.
   */
  'as'?: 'menu' | 'div' | 'card'

  /** Positioning options. Default: { placement: 'auto' }. */
  'placement'?: PopoverPositionOptions

  /** Optional popover ID (auto-generated if omitted). */
  'id'?: string

  /** Ref callback for programmatic hidePopover(). */
  'popoverRef'?: (el: HTMLElement) => void

  /** CSS class on the popover element. */
  'class'?: string

  /**
   * The trigger is a FIELD, so the menu is capped at its width.
   *
   * A field's menu is the open form of the control the user clicked: the two
   * read as one control when their edges line up, and a row longer than the
   * field clips instead of pushing the box out over the page.
   *
   * Opt-in, because a trigger is not always a field. A kebab is a 24px icon
   * button, and a menu capped at that leaves a column too narrow to read a
   * single row. Only a caller that knows the shape of its own trigger asks --
   * `LoadingMenu` does, and nothing else should without being one.
   *
   * The HEIGHT clamp needs no flag: it follows the dialog, and every menu
   * belongs inside whatever box holds it.
   */
  'matchTriggerWidth'?: boolean

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
      {/* The raw style, not `ClippedText`: `label` is arbitrary JSX, and
          `ClippedText` renders a plain string. Every live caller passes one
          today, so narrowing `label` to `string` would let this take the
          component -- that changes a shared prop contract, so it is the
          author's decision, not a mechanical swap. */}
      <span class={clippedText}>{props.label}</span>
      <Show when={props.shortcut}>
        {shortcut => <span class={menuItemDetail}>{shortcut()}</span>}
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
  'data-testid'?: string
  /**
   * Rendered inside the button, between the checked indicator and the label.
   *
   * For an option whose identity is not fully carried by its name -- a theme's
   * colour swatch. It goes through this component rather than around it because
   * the button, its role and its `aria-checked` all belong to the item; a caller
   * that wrapped its own markup in a `role="menuitemradio"` would announce the
   * option with no on/off state, which is what owning the button prevents.
   *
   * Decorative by contract: the label carries the accessible name, so a swatch
   * passed here sets `aria-hidden`.
   */
  'leading'?: JSXElement
  /**
   * A short note at the RIGHT end of the row -- the age of a session, the
   * shortcut of a command.
   *
   * It never shrinks and never wraps, so it stays readable while a long `label`
   * clips beside it. That is the whole reason it is a slot of its own rather
   * than text a caller appends to `label`: text inside the label is inside the
   * ellipsis, so the row that most needs its timestamp is the row that loses it.
   *
   * Content, not decoration, so it is NOT `aria-hidden`: a screen reader reads
   * it as part of the item, which is what `leading` deliberately withholds for
   * a colour swatch.
   *
   * An ACCESSOR, not an element. A JSX prop compiles to a getter, so a caller
   * whose detail is an expression rebuilds it on every read -- and a presence
   * test plus a draw is two reads, which produced one element to test and a
   * different one on screen. A function reference is free to read, so presence
   * is `!== undefined` and the element is built exactly once, where it is drawn.
   * That also lets a detail be a legitimately falsy `0` or `''`, which a
   * truthiness gate would have dropped.
   */
  'detail'?: () => JSXElement
  /**
   * Show the whole `label` in a tooltip once the row clips it.
   *
   * OFF by default, and the default is not caution about the cost. An item
   * whose CALLER wraps it in a `Tooltip` -- `~/components/chat/settingsShared`
   * puts the option's description, or the reason the group is read-only, on the
   * whole button -- can hold only one open tooltip at a time, so a second one
   * that repeats the label verbatim would dismiss the description that explains
   * the option. A caller that wraps no tooltip of its own sets this, and its
   * clipped labels keep a route back.
   */
  'revealClippedLabel'?: boolean
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
  // Derived from the item's own test id, so a test can address the LABEL rather
  // than the whole row -- whose text now also holds the detail, and whose detail
  // may be a clock that ticks while the test reads it.
  const labelTestId = () => {
    const testId = props['data-testid']
    return testId === undefined ? undefined : `${testId}-label`
  }

  return (
    <button
      // A <button> defaults to type="submit". This item toggles a preference, so
      // inside a <form> the default would also submit it. The code this replaced
      // called preventDefault() for that reason; declaring the type removes the
      // default action instead of cancelling it at every call site.
      type="button"
      role={props.kind === 'checkbox' ? 'menuitemcheckbox' : 'menuitemradio'}
      aria-checked={props.checked}
      disabled={props.disabled}
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
        {/* `aria-hidden` HERE, not at each caller. `leading` is decorative by
            contract, and a contract each caller restates is one a caller can
            forget -- the same reason this component owns the button rather than
            letting a caller supply one. `display: contents` keeps the wrapper
            out of the flex layout, so the swatch sits exactly where it did. */}
        <span aria-hidden="true" style={{ display: 'contents' }}>{props.leading}</span>
        {/* Two spellings of one label, and the caller picks. `ClippedText`
            pairs the ellipsis with the tooltip that reads the rest, which is
            what a row of unbounded titles needs. The raw style is for the
            caller that wraps this whole button in a `Tooltip` of its own: a
            second tooltip would dismiss that one -- `Tooltip` keeps at most one
            open -- and replace the option's DESCRIPTION with a verbatim repeat
            of the label. See `revealClippedLabel`. */}
        <Show
          when={props.revealClippedLabel}
          fallback={<span class={clippedText} data-testid={labelTestId()}>{props.label}</span>}
        >
          <ClippedText text={props.label} testId={labelTestId()} />
        </Show>
        {/* The callback form, and it is load-bearing: `Show` memoizes `when`,
            so `props.detail` is read ONCE per tracking cycle. A caller whose
            detail is an expression (`LoadingMenu` renders one from the option)
            has a getter behind that prop, and a second read there builds a
            second element -- one to test for presence and a different one on
            screen. An empty detail draws nothing, which is what an option with
            no note wants. */}
        <Show when={props.detail !== undefined}>
          <span class={menuItemDetail}>{props.detail?.()}</span>
        </Show>
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

  // Where a right-click or long-press that opened this menu landed. Cleared when
  // the popover closes, so the next kebab click re-anchors to the kebab.
  const [pressAnchor, setPressAnchor] = createSignal<ContextMenuPress>()

  const setTriggerRef = (el: HTMLElement) => {
    triggerEl = el
    // Mark the trigger so an ancestor popover's dismiss handler can recognize
    // it. A nested dropdown's trigger is a DOM child of the parent popover, so
    // without this the parent hides itself on the very click that opens the
    // submenu — and hiding a popover hides its descendants too.
    el.setAttribute(TRIGGER_ATTR, '')
  }

  // Resolve the positioning anchor. A press anchor wins while it is set, because
  // the menu the user just opened by right-click or long-press must point at that
  // press and not at the kebab they never touched.
  const getAnchor = (): PopoverAnchor | undefined => {
    const press = pressAnchor()
    if (press)
      return pressAnchorRect(press)
    // For the JSX-element trigger path, triggerEl is the display:contents wrapper
    // whose bounding rect is zero. Fall back to its first visible child element.
    if (triggerEl) {
      const rect = triggerEl.getBoundingClientRect()
      if (rect.width === 0 && rect.height === 0 && triggerEl.firstElementChild) {
        return triggerEl.firstElementChild as HTMLElement
      }
      return triggerEl
    }
    return props.anchorRef?.()
  }

  // `aria-expanded` is an element concern; a press anchor is a bare rect and has no
  // attributes to carry it.
  const getAnchorElement = (): Element | undefined => {
    const anchor = getAnchor()
    return anchor instanceof Element ? anchor : undefined
  }

  /**
   * Whether this popover is MENU-shaped: a list of rows, sized to follow the
   * control it opens from.
   *
   * A card popover is not. It carries its own content and its own width, so
   * capping it at the trigger would squeeze an agent-info card into a kebab.
   */
  const isMenuShaped = () => props.as === undefined || props.as === 'menu' || props.as === 'div'

  /**
   * Measure the caps a menu popover follows: the trigger's width, and the height
   * of the dialog that holds it. See `popoverMenuClamp`.
   *
   * Measured rather than inherited, because the popover is in the top layer: it
   * is positioned against the viewport and its size answers to nothing in the
   * form the trigger sits in. The dialog is found FROM the trigger, so the box a
   * menu belongs to is a fact about where that trigger sits and not something
   * each call site has to declare -- and a menu no dialog holds writes nothing,
   * leaving the stylesheet's viewport fallback.
   */
  const applyCaps = () => {
    if (!popoverEl || !isMenuShaped())
      return
    const anchor = getAnchorElement()
    if (!anchor)
      return
    // The width only where the caller says its trigger is a FIELD. A kebab is a
    // 24px icon button, and capping its menu at that leaves every row
    // unreadable -- so this is not something a menu may assume about its own
    // trigger.
    if (props.matchTriggerWidth)
      popoverEl.style.setProperty(TRIGGER_WIDTH_VAR, `${anchor.getBoundingClientRect().width}px`)
    const dialog = anchor.closest('dialog')
    if (dialog)
      popoverEl.style.setProperty(DIALOG_HEIGHT_VAR, `${dialog.getBoundingClientRect().height}px`)
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
    // A press anchor is a frozen point in the viewport, not an element: the row
    // the user pressed scrolled away, and re-running the arithmetic would
    // attach the menu to that stale point over whatever scrolled into place. Every
    // OS context menu closes on scroll instead, and so does this one. An
    // element anchor follows its element, so it keeps repositioning.
    if (!(getAnchor() instanceof Element)) {
      popoverEl?.hidePopover()
      return
    }
    reposition()
  }

  // --- keyboard navigation -------------------------------------------------
  //
  // A native `<select>` supplied all of this for free, and its removal took all
  // of it: arrow keys, Home/End, and type-ahead. A keyboard user had to Tab
  // through every option, which on a twelve-shell machine is twelve presses to
  // reach the last one. It lives HERE rather than in `LoadingMenu` because
  // every menu popover has items, not just the ones that wrap a list.
  //
  // OAT's own `ot-dropdown` roving-focus code cannot serve: it stops when there
  // is no `[popovertarget]` (this component deliberately renders none), and it
  // queries `[role="menuitem"]` while these items carry `menuitemradio`.

  const menuItems = (): HTMLElement[] =>
    popoverEl ? [...popoverEl.querySelectorAll<HTMLElement>(MENU_ITEM_SELECTOR)] : []

  /**
   * Move focus to `index`, wrapping at both ends.
   *
   * Wrapping is what the native control does, and it is what makes End reachable
   * from the top with one press.
   */
  const focusItemAt = (index: number) => {
    const items = menuItems()
    if (items.length === 0)
      return
    const wrapped = ((index % items.length) + items.length) % items.length
    items[wrapped]?.focus()
  }

  /** The item focus sits on now, or -1 when focus is elsewhere in the popover. */
  const focusedItemIndex = (): number => {
    const active = document.activeElement as HTMLElement | null
    return active ? menuItems().indexOf(active) : -1
  }

  let typeAheadBuffer = ''
  let typeAheadTimer: ReturnType<typeof setTimeout> | undefined

  /**
   * Focus the first item whose label starts with the buffered keystrokes.
   *
   * Searches from the item AFTER the focused one and wraps, so typing the same
   * letter repeatedly cycles through the items that share it -- which is what a
   * `<select>` does, and the reason this code does not simply reset the buffer
   * per key.
   */
  const typeAhead = (key: string) => {
    clearTimeout(typeAheadTimer)
    typeAheadBuffer += key.toLowerCase()
    typeAheadTimer = setTimeout(() => (typeAheadBuffer = ''), TYPE_AHEAD_RESET_MS)

    const items = menuItems()
    if (items.length === 0)
      return
    // A repeat of one letter cycles; a longer buffer re-searches from the
    // current item so the match that the user extends stays a candidate.
    const repeated = typeAheadBuffer.length > 1
      && [...typeAheadBuffer].every(c => c === typeAheadBuffer[0])
    const from = focusedItemIndex()
    const start = typeAheadBuffer.length === 1 || repeated ? from + 1 : from
    const needle = repeated ? typeAheadBuffer[0]! : typeAheadBuffer
    for (let i = 0; i < items.length; i++) {
      const item = items[(((start + i) % items.length) + items.length) % items.length]!
      if ((item.textContent ?? '').trim().toLowerCase().startsWith(needle)) {
        item.focus()
        return
      }
    }
  }

  const handleMenuKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      popoverEl?.hidePopover()
      return
    }
    // A panel of content is not a set of commands, so it takes no roving focus
    // -- its own controls Tab in the ordinary way. `as="div"` alone does NOT
    // opt out, because that is also how a FILTERED menu renders.
    if (props.as === 'card')
      return

    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        focusItemAt(focusedItemIndex() + 1)
        return
      case 'ArrowUp':
        e.preventDefault()
        focusItemAt(focusedItemIndex() - 1)
        return
      case 'Home':
        e.preventDefault()
        focusItemAt(0)
        return
      case 'End':
        e.preventDefault()
        focusItemAt(menuItems().length - 1)
        return
    }

    // Type-ahead. A single printable character with no modifier -- Ctrl/Meta
    // combinations are shortcuts, and a filter box (which reads its own keys)
    // is where the character belongs when one has focus.
    if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey && e.key !== ' ') {
      const active = document.activeElement as HTMLElement | null
      const typingIntoAField = active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement
      if (typingIntoAField)
        return
      e.preventDefault()
      typeAhead(e.key)
    }
  }

  /**
   * The outside-press dismissal a `manual` popover has to supply itself.
   *
   * Armed only for a menu that a long press opened (see `showPressMenu`), and
   * disarmed the moment it closes, so no listener remains while the app is
   * idle. On `pointerdown`, which is where the native pass acts too, and in the
   * CAPTURE phase so a row that swallows its own presses cannot hide one.
   *
   * The finger that OPENED the menu never reaches this: its `pointerdown`
   * happened before the menu existed, and its release is a `pointerup`.
   */
  let disarmPressDismiss: (() => void) | undefined

  const armPressDismiss = () => {
    disarmPressDismiss?.()
    const onOutsidePress = (e: Event) => {
      if (holdIsOverMenu())
        return
      if (!popoverEl?.matches(':popover-open'))
        return
      if (e.composedPath().includes(popoverEl))
        return
      popoverEl.hidePopover()
    }
    document.addEventListener('pointerdown', onOutsidePress, true)
    disarmPressDismiss = () => {
      document.removeEventListener('pointerdown', onOutsidePress, true)
      disarmPressDismiss = undefined
    }
  }

  const handleToggle = (_e: Event) => {
    // Read the post-transition state from the popover element rather than
    // ToggleEvent.newState. Spec-wise both are equivalent (the toggle event
    // fires after the state change), but the jsdom popover stub dispatches
    // a plain Event without the ToggleEvent shape — checking `:popover-open`
    // works in both environments.
    const opening = popoverEl?.matches(':popover-open') ?? false
    // A hold that finds an already-open kebab menu hides it only to swap
    // `popover="auto"` to `manual`. That hide is not a user close.
    if (!opening && swappingHoldMode)
      return
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
      if (popoverEl) {
        resizeObserver?.disconnect()
        // The SAME observer measures the caps below, so one delivery does one
        // read-and-write pass. `createRafResizeObserver`, because `applyCaps`
        // writes onto the popover this same observer watches: a write made
        // inside the delivery phase resizes an observed element, which is the
        // "ResizeObserver loop completed with undelivered notifications" case
        // that helper defers a frame to avoid.
        resizeObserver = createRafResizeObserver(() => {
          applyCaps()
          reposition()
        })
        resizeObserver?.observe(popoverEl)
        // The anchor and the dialog that holds it, because a cap that is
        // measured once at open is wrong the moment either changes -- a window
        // dragged narrower, a dialog that relayouts under a phone's keyboard --
        // and a stale cap is invisible until the day it is wrong.
        const anchor = getAnchorElement()
        if (anchor)
          resizeObserver?.observe(anchor)
        const dialog = anchor?.closest('dialog')
        if (dialog)
          resizeObserver?.observe(dialog)
      }
      applyCaps()

      getAnchorElement()?.setAttribute('aria-expanded', 'true')

      // Put focus INSIDE the popover, so the keys below reach it.
      //
      // `popover="auto"` moves focus nowhere, so without this the trigger keeps
      // it and every arrow key and every typed character goes to the document
      // -- the type-ahead a `<select>` gave for free would need the user to
      // Tab in first, which is the thing it exists to avoid.
      //
      // The POPOVER, not the first item: focusing an item would announce it as
      // selected and, in a radio menu, read as a change the user did not make.
      // A caller that wants a specific element focused (LoadingMenu's filter
      // box) does it in its own `onToggle`, which runs after this.
      //
      // Deferred one frame, because the popover is not yet visible when
      // `toggle` fires and `focus()` on a hidden element does nothing.
      requestAnimationFrame(() => {
        if (holdIsOverMenu())
          return
        if (popoverEl?.matches(':popover-open') && !popoverEl.contains(document.activeElement))
          popoverEl.focus({ preventScroll: true })
      })
    }
    else {
      // Drop a half-typed type-ahead buffer, so reopening the menu does not
      // resume a search the user abandoned.
      clearTimeout(typeAheadTimer)
      typeAheadBuffer = ''
      window.removeEventListener('scroll', repositionOnExternalScroll, true)
      resizeObserver?.disconnect()
      resizeObserver = undefined

      getAnchorElement()?.setAttribute('aria-expanded', 'false')
      // Drop the press so the next kebab click anchors to the kebab again. A menu
      // opened by a press has no element anchor to clear `aria-expanded` on -- the
      // trigger's own copy is reactive on `isOpen()` and falls with it.
      setPressAnchor(undefined)
      // Hand the popover back to the platform. A long press opened it as
      // `manual` and dismissed it by hand (see `showPressMenu`); every other way
      // of opening this menu wants the native light dismiss, and the attribute
      // can only be swapped while it is closed -- which it now is.
      disarmPressDismiss?.()
      popoverEl?.setAttribute('popover', 'auto')
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
    // Track the ANCHOR, not just the boolean: an already-open menu whose anchor
    // changed (a singleton host serving a second row's press) repositions IN
    // PLACE, so no host has to close and reopen just to re-trigger this effect.
    // A caller whose anchor is a stable element gains no new subscription -- the
    // anchor read was already part of every reposition.
    createEffect(on(() => [props.open?.(), props.anchorRef?.()], ([shouldOpen]) => {
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
        // The same choice the gesture path makes, because a singleton host
        // opens THROUGH here: the chat list's shared message menu is driven by
        // this `open` accessor, and its opens come from a long press whose
        // finger is still down. See `showMenuPopover`.
        if (showMenuPopover(popoverEl))
          armPressDismiss()
        // Position synchronously, before the browser paints this frame, so the
        // popover never appears at the UA-default position and then jumps. The
        // content is already in the DOM (rendered before this effect), so it
        // measures at its real size here. The rAF reposition in handleToggle + the
        // ResizeObserver then refine it for any late layout.
        reposition()
      }
      else if (shouldOpen && nativeOpen) {
        // Open the whole time, anchor moved: re-anchor with no teardown. The
        // ResizeObserver from the first open re-refines once swapped content
        // settles its size, same as it does after a scroll.
        reposition()
      }
      else if (!shouldOpen && nativeOpen) {
        popoverEl.hidePopover()
      }
    }))
  }

  // Right-click / long-press on the owning row. See `scheduleOpen` in
  // ~/components/common/contextMenuGesture.ts for the two calls a touch hold
  // makes and why.
  createEffect(() => {
    const el = props.contextMenuFor?.()
    if (!el)
      return
    const detach = attachContextMenuGesture(el, {
      onOpen: (press) => {
        setPressAnchor(press)
        if (!popoverEl)
          return
        if (popoverEl.matches(':popover-open') && !holdIsOverMenu()) {
          reposition()
          return
        }
        if (showMenuPopover(popoverEl))
          armPressDismiss()
        reposition()
        setTimeout(reposition, 0)
      },
      onCancel: () => {
        popoverEl?.hidePopover()
      },
    })
    onCleanup(detach)
  })

  onCleanup(() => {
    popoverEl?.removeEventListener('toggle', handleToggle)
    window.removeEventListener('scroll', repositionOnExternalScroll, true)
    resizeObserver?.disconnect()
    // A menu unmounted while open never fires its closing `toggle`, so this
    // cleanup drops the document listener that dismisses a press-opened one too.
    disarmPressDismiss?.()
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

  const popoverClass = () => {
    const shape = props.as === 'card'
      ? popoverCard
      : (props.matchTriggerWidth ? popoverFieldMenuClamp : popoverMenuClamp)
    return props.class ? `${shape} ${props.class}` : shape
  }

  // `data-headless` marks a dropdown with no trigger -- one opened only by
  // right-click or long press. The host then collapses to `display: contents` and
  // adds no flex item to the row that mounts it; see ~/styles/popover.css.ts.
  //
  // `attr:` is required, not stylistic: `ot-dropdown` has a dash in its name, so
  // Solid treats it as a custom element and assigns unknown props as PROPERTIES.
  // Without the namespace this sets `el.dataHeadless` and no attribute, and the
  // CSS rule never matches.
  return (
    <ot-dropdown attr:data-headless={props.trigger === undefined ? 'true' : undefined}>
      {renderTrigger()}
      {/* `menu` (default) and `div` popovers differ ONLY by tag; everything else (the
          popover attr, id, ref, class, testid, and the Escape/outside-click dismiss
          handlers) is identical, so render via Dynamic instead of two byte-identical
          branches that could drift. */}
      <Dynamic
        component={props.as === undefined || props.as === 'menu' ? 'menu' : 'div'}
        popover="auto"
        id={menuId}
        ref={popoverRefCallback}
        // A card takes `popoverCard`; every other shape is a MENU and takes the
        // menu clamp, which is what stops a long list running off the bottom of
        // the screen with the rows past the edge unreachable. Before it, only
        // the card branch was clamped at all.
        class={popoverClass()}
        data-testid={props['data-testid']}
        aria-label={props['aria-label']}
        // `tabIndex` so the popover itself can hold focus. That is what makes
        // arrow keys and type-ahead work WITHOUT the user first Tabbing into
        // the list: `popover="auto"` moves focus nowhere on its own, so on open
        // the focus stays on the trigger and every key went to the document.
        // A menu takes it; a card is a panel whose own controls Tab normally.
        tabIndex={props.as === 'card' ? undefined : -1}
        onKeyDown={handleMenuKeyDown}
        onClick={(e: MouseEvent) => {
          e.stopPropagation()
          // A `div` popover is a panel of content, not a set of commands, so no
          // click inside it dismisses it -- see `as`. A panel that dismissed on
          // a click could not hold selectable text: the press starts the
          // selection and the release closes the popover under it.
          if (props.as === 'div' || props.as === 'card')
            return
          // A click on a nested dropdown's trigger opens that submenu; it must
          // not also dismiss this popover, which would close the submenu with
          // it. Every other click inside a menu is an activation, so it closes
          // the menu as usual.
          if ((e.target as Element | null)?.closest?.(`[${TRIGGER_ATTR}]`))
            return
          // The item's own handler runs first and may have closed this popover
          // already. `hidePopover()` on a popover that is not showing throws
          // InvalidStateError, so read the native state before the call.
          if (popoverEl?.matches(':popover-open'))
            popoverEl.hidePopover()
        }}
      >
        {props.children}
      </Dynamic>
    </ot-dropdown>
  )
}
