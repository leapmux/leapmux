import type { Component, JSX } from 'solid-js'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, createSignal, For, Match, Show, Switch } from 'solid-js'
import { ClippedText } from '~/components/common/ClippedText'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { menuItemDetail } from '~/styles/shared.css'
import * as styles from './LoadingMenu.css'

/**
 * The short note one option carries at the right end of its row: the age of a
 * session, the size of a file.
 *
 * It is a slot of its own rather than text appended to the label, because the
 * label CLIPS. Text inside the label is inside the ellipsis, so the row with a
 * title long enough to need its timestamp is the row that loses it.
 */
export interface LoadingMenuOptionDetail {
  /**
   * The text form. The filter matches it beside the label, and it is what the
   * row shows unless {@link LoadingMenuOptionDetail.render} replaces it.
   *
   * A FUNCTION, read at FILTER time, because a detail can be a moving value.
   * `SessionSelect` draws a live `RelativeTime` and its text is that same age:
   * held as a plain string, the string froze at whatever instant the option
   * list was last built while the element beside it kept ticking, so a row
   * that read "4h ago" was matched against "3h ago" and typing what was on
   * screen emptied the list.
   */
  text: () => string
  /**
   * Draws the detail in place of {@link LoadingMenuOptionDetail.text} -- a live
   * `RelativeTime`, whose own tooltip states the full timestamp.
   *
   * A FUNCTION and not an element, for two reasons. The trigger and the row
   * both draw the detail, and one DOM node cannot sit in two places. And an
   * element built with the option list is built for every row of every menu on
   * screen, open or not, because `DropdownMenu` renders its children eagerly.
   */
  render?: () => JSX.Element
}

/** One choice. `group` puts it under a heading; entries keep their given order. */
export interface LoadingMenuOption {
  value: string
  label: string
  group?: string
  detail?: LoadingMenuOptionDetail
  /**
   * This row survives the filter, whatever the query.
   *
   * For a row that is not one of the things the list holds but a way OUT of it:
   * `SessionSelect`'s "Start a new session" and "Enter a session ID...". A query
   * that matches no session is exactly when a user needs those, and filtering
   * them left an empty menu whose only escape was to clear the query.
   */
  pinned?: boolean
}

export interface LoadingMenuProps {
  'value': string
  'onChange': (value: string) => void
  /**
   * The option list is still loading, and this is what the trigger and the menu
   * body show while it is. Omit it when the caller holds its list already.
   *
   * ONE prop, not a `loading` flag beside a `loadingLabel`. The pair let a
   * caller arm the flag with no label, which renders an empty trigger, and let
   * two callers pass a label that can never render because their flag is a
   * literal `false`. Presence is the state, so neither mistake can be written.
   */
  'loadingLabel'?: string
  'emptyLabel': string
  /**
   * Caller-controlled disabled flag. OR'd with the internal disable on
   * loading / empty, so callers never have to repeat that correlation.
   */
  'disabled'?: boolean
  /** Accessible name for the trigger and the menu. */
  'ariaLabel': string
  /**
   * The menu's value fails the field's validation, so the trigger announces
   * itself as invalid.
   *
   * A menu normally cannot hold an unacceptable value, because every option
   * came from the field. It matters where a menu SHARES a field with a text
   * input and the value survives the swap between them — `ResumeSessionField`
   * is that case.
   */
  'ariaInvalid'?: boolean
  /**
   * The id of the node that states why the value is unacceptable, so the
   * trigger points at it.
   *
   * Without it the error is announced ONCE, as the alert appears, and a screen
   * reader user who returns to the trigger afterwards finds a control that says
   * nothing is wrong beside a Create button that refuses to run.
   */
  'ariaDescribedBy'?: string
  /** Shown on the trigger when `value` matches no option — the "pick one" prompt. */
  'placeholder'?: string
  'options': LoadingMenuOption[]
  /**
   * Offer a filter box above the list.
   *
   * DERIVED from the option count when the caller says nothing: a list of
   * FILTER_MIN_OPTIONS or more gets one. Scanning a long menu by eye is the
   * work the filter removes, and which menus are long is a property of the
   * DATA, not something each call site should have to predict -- `BranchSelect`
   * knew its list was unbounded, but a machine with twenty shells produces a
   * long `ShellSelector` that nobody declared.
   *
   * Pass it explicitly to override in either direction. A short list gets no
   * filter, because a filter over five entries is noise.
   */
  'filter'?: boolean
  /**
   * Called when the menu opens.
   *
   * The seam the native `<select>` had as `onFocus` + `onPointerDown`:
   * `WorkerSelector` warms the metadata for the workers it is ABOUT TO LIST.
   *
   * Deliberately tied to the open, not to the worker list arriving. The
   * prefetch skips the selected worker and fans out an RPC per remaining
   * online worker, so firing it whenever the list changes turns a lazy warm-up
   * into a storm on every refresh -- and the trigger's own label never needed
   * it, because the selected worker's metadata arrives by another path.
   */
  'onOpen'?: () => void
  'data-testid'?: string
}

/**
 * The option count at which a menu grows a filter box.
 *
 * Above a dozen, finding one entry by eye is real work and the filter is the
 * cheapest way to cut it. Below, the box costs a row of chrome and a focus stop
 * for a list the user can read at a glance.
 *
 * `EnumControl` switches from pills to a menu at 4; this is the second
 * threshold on the same axis, and the two are independent on purpose -- one is
 * about how much horizontal room a row of pills needs, this is about how long a
 * list a reader can scan.
 */
const FILTER_MIN_OPTIONS = 12

/**
 * The renderer for one option's detail: the caller's own element when it gave
 * one, and the plain text otherwise. Undefined for an option with no detail.
 *
 * One function for the two sites that draw a detail -- the row and the trigger
 * -- so neither can forget that `render` takes precedence over `text`. It hands
 * back a FUNCTION rather than an element, so the element is built where it is
 * drawn: the trigger and the row each need their own, and one DOM node cannot
 * sit in two places.
 *
 * `live` false takes the TEXT even when a renderer exists. A caller's element
 * can be expensive -- `SessionSelect`'s is a live `RelativeTime` that subscribes
 * to the shared ticker -- and a row inside a menu nobody has opened should not
 * pay for it. The two read the same, because the text IS what the element draws.
 */
function detailRenderer(
  detail: LoadingMenuOptionDetail | undefined,
  live = true,
): (() => JSX.Element) | undefined {
  if (detail === undefined)
    return undefined
  return () => (live && detail.render ? detail.render() : detail.text())
}

/**
 * A one-of-N menu that survives the loading -> loaded transition.
 *
 * Replaces the native `<select>` this used to wrap; see the dropdown rule in
 * CLAUDE.md. The swap deleted a whole class of bug with it: a `<select>` keeps
 * its selection in `selectedIndex`, which is BROWSER state, so the old component
 * needed an effect that re-applied `value` every time the option children were
 * swapped -- otherwise a list that arrived after the caller had already seeded
 * `value` left the field showing the first option and disagreeing with form
 * state. A menu derives its checked item from `value` on every render, so there
 * is nothing to resynchronise and no effect to get wrong.
 *
 * It still normalizes the disable rule across every "loading select" in the app:
 * disabled when the caller asks, when loading, or when the list has nothing to
 * pick.
 */
export const LoadingMenu: Component<LoadingMenuProps> = (props) => {
  const [query, setQuery] = createSignal('')
  // Whether the menu has ever been opened; see `items`. One-way: it never goes
  // back to false, so a reopen mounts nothing again.
  const [everOpened, setEverOpened] = createSignal(false)
  // A signal, not a plain `let`: `select` reads it to dismiss a filtered menu.
  const [popoverEl, setPopoverEl] = createSignal<HTMLElement>()
  let filterEl: HTMLInputElement | undefined

  const testId = () => props['data-testid'] ?? 'loading-menu'

  /** Loading is exactly "a loading label was given". */
  const loading = () => props.loadingLabel !== undefined

  /**
   * Whether this menu shows a filter box.
   *
   * The caller's word when it gave one; otherwise the option count decides.
   * Derived rather than declared, because how long a list is depends on the
   * user's data -- a repository's branches, a machine's shells -- and not on
   * anything the call site knows when it is written.
   */
  const filtering = () => props.filter ?? props.options.length >= FILTER_MIN_OPTIONS

  /**
   * Whether there is nothing to choose from.
   *
   * DERIVED, not declared. It was a required prop, and every caller answered it
   * with the length of the very list it passed as `options` -- so the prop was
   * a second copy of a fact `options` already carries, and the two could
   * disagree. `BranchSelect` made them disagree: it injected its own "Select a
   * branch..." row into `options`, so in a repository with no branches the list
   * was one entry long while `isEmpty` said true, and the menu disabled itself
   * around a row it had just rendered. That row is what `placeholder` is for,
   * and BranchSelect now passes one.
   *
   * It reads the FULL list rather than `visible()`: a filter that matches
   * nothing leaves plenty to choose from, and disabling the trigger under the
   * user's own query would trap them with no way to clear it.
   */
  const isEmpty = () => props.options.length === 0

  const disabled = () => (props.disabled ?? false) || loading() || isEmpty()

  const visible = createMemo(() => {
    // eslint-disable-next-line solid/reactivity -- `q` is read by the props.options.filter callback below, in this same memo evaluation
    const q = query().trim().toLowerCase()
    if (!filtering() || q === '')
      return props.options
    // The DETAIL matches too, and it has to: the age of a session moved out of
    // the label and into that slot, so a filter that read the label alone would
    // silently drop the "sessions from days ago" search the label used to serve.
    //
    // A PINNED option survives every query. Such a row is not one of the things
    // the list holds -- it is a way OUT of the list, and `SessionSelect` marks
    // both of its own that way. Filtering them removed the escape hatch at the
    // one moment it is needed: a user whose session is not here types its title,
    // gets "No matches", and now has neither "Enter a session ID..." nor "Start
    // a new session" to reach. Clearing the query was the only route back.
    return props.options.filter(o =>
      o.pinned === true
      || o.label.toLowerCase().includes(q)
      || (o.detail?.text().toLowerCase().includes(q) ?? false),
    )
  })

  /** The option the value names, or undefined when the list does not hold it. */
  const selected = () => props.options.find(o => o.value === props.value)

  /**
   * What the trigger shows: its label, and the detail beside it. Four states,
   * not three.
   *
   * A value that matches NO option is its own case, and it is not the empty
   * list: falling through to `emptyLabel` made the trigger read "No workers
   * online" or "No options" above a populated menu, which states the opposite
   * of what the user can see. The raw value is the truthful answer and it is
   * what the reader needs in order to repair the setting, so an unmatched
   * non-empty value shows itself.
   *
   * `placeholder` stays for the genuinely EMPTY value -- the "pick one" prompt
   * that `BranchSelect` and `WorktreeSelect` render before a choice is made.
   *
   * ONE accessor for both, because they are one decision. The trigger is the
   * closed form of the selected ROW, so it carries the same two columns -- and
   * a detail belongs beside a REAL match only, since the other three states
   * describe the LIST rather than a row. Derived twice, a fifth state added to
   * the label would reach the detail's guard silently or not at all.
   */
  const triggerState = (): { label: string, detail?: LoadingMenuOptionDetail } => {
    const loadingLabel = props.loadingLabel
    if (loadingLabel !== undefined)
      return { label: loadingLabel }
    if (isEmpty())
      return { label: props.emptyLabel }
    const match = selected()
    if (match)
      return { label: match.label, detail: match.detail }
    if (props.value === '')
      return { label: props.placeholder ?? props.emptyLabel }
    return { label: props.value }
  }

  const select = (value: string) => {
    props.onChange(value)
    // A filtered menu renders `as="div"` so a click in the filter box does not
    // dismiss it, which also means an item click does not either. Closing here
    // is what that trade costs, and only the item knows the action ran.
    if (filtering()) {
      popoverEl()?.hidePopover()
      setQuery('')
    }
  }

  /**
   * The list, and the two states that must REPLACE it rather than sit beside it.
   *
   * `loading` tears the options down. The predecessor restricted its children the
   * same way, and dropping that gate left a stale list mounted and clickable:
   * a fetcher keeps the previous entries until its next success, `DropdownMenu`
   * renders children eagerly, and the item rows take no `disabled` -- so a
   * refresh fired while the popover is open (F5 and $mod+r carry no `when`
   * clause) left the user picking a branch from the repository that was
   * replaced. Disabling the TRIGGER cannot help once the popover is already
   * open.
   */
  const items = () => (
    <Switch>
      <Match when={props.loadingLabel !== undefined}>
        <div class={styles.emptyNote}>{props.loadingLabel}</div>
      </Match>
      <Match when={isEmpty()}>
        <div class={styles.emptyNote}>{props.emptyLabel}</div>
      </Match>
      <Match when={visible().length === 0}>
        <div class={styles.emptyNote}>No matches</div>
      </Match>
      <Match when={visible().length > 0}>
        <For each={visible()}>
          {(option, i) => (
            <>
              {/* A heading whenever the group changes, so Local and Remote read
                  apart the way `<optgroup>` used to render them. */}
              <Show when={option.group && option.group !== visible()[i() - 1]?.group}>
                <div class={styles.groupHeading}>{option.group}</div>
              </Show>
              <DropdownMenuCheckableItem
                kind="radio"
                label={option.label}
                // This menu wraps no tooltip of its own around an item, so the
                // label may own one -- and it must, because the rows hold
                // whatever the user's data is long enough to be: a branch name,
                // a session title. Clipped with no route back is the state a
                // tooltip exists to prevent.
                //
                // Deferred to the first OPEN, and that is the whole reason this
                // is a prop rather than always-on. `DropdownMenu` renders its
                // children on MOUNT, and `ClippedText` wraps every instance in
                // a `Tooltip` -- a MutationObserver and listeners on two
                // elements, per row. Paid eagerly that is one such allocation
                // per option of every menu on screen, open or not: fifty for a
                // full session list, and `BranchSelect`'s list has no upper
                // limit at all. A row nobody has seen needs no route back.
                revealClippedLabel={everOpened()}
                // Same deferral, same reason: `SessionSelect`'s detail is a live
                // `RelativeTime` that subscribes to the shared ticker, so an
                // unopened picker held one subscriber per session. The text form
                // renders until then, so the row reads the same either way.
                detail={detailRenderer(option.detail, everOpened())}
                checked={option.value === props.value}
                data-testid={`loading-menu-option-${option.value}`}
                onSelect={() => select(option.value)}
              />
            </>
          )}
        </For>
      </Match>
    </Switch>
  )

  return (
    <DropdownMenu
      aria-label={props.ariaLabel}
      as={filtering() ? 'div' : 'menu'}
      // This menu's trigger IS a field -- it is shaped like the input it
      // replaces -- so the popover follows its width. See the prop.
      matchTriggerWidth
      popoverRef={setPopoverEl}
      onToggle={(open) => {
        if (open) {
          setEverOpened(true)
          props.onOpen?.()
          // Put the caret in the filter box. `popover="auto"` moves focus
          // nowhere on its own, so without this the filter buys back none of
          // the type-ahead a `<select>` gave: the user opens the menu, types,
          // and nothing happens until they click into the box. Deferred to the
          // next frame because the popover is not yet visible when `toggle`
          // fires, and `focus()` on a hidden element does nothing.
          requestAnimationFrame(() => filterEl?.focus())
        }
        else {
          setQuery('')
        }
      }}
      data-testid={testId()}
      trigger={triggerProps => (
        <button
          {...triggerProps}
          type="button"
          class={styles.trigger}
          aria-label={props.ariaLabel}
          aria-invalid={props.ariaInvalid ? 'true' : undefined}
          aria-describedby={props.ariaDescribedBy}
          // Derived from the menu's own id, because `DropdownMenu` puts that id
          // on the POPOVER and renders the trigger as its sibling -- so a
          // caller cannot reach the trigger by descending into the menu.
          data-testid={`${testId()}-trigger`}
          data-value={props.value}
          disabled={disabled()}
        >
          {/* `ClippedText`, so the title of the picked session is readable on
              hover once the field is too narrow to hold it. The trigger's own
              `aria-label` names the control, so the tooltip adds the value and
              never renames the button. */}
          <ClippedText text={triggerState().label} class={styles.triggerLabel} />
          <Show when={triggerState().detail}>
            {/* The row's own class, because the trigger IS the closed form of
                the selected row: the same label, the same trailing note. */}
            {detail => <span class={menuItemDetail}>{detailRenderer(detail())?.()}</span>}
          </Show>
          <Icon icon={ChevronDown} size="xs" aria-hidden="true" />
        </button>
      )}
    >
      <Show when={filtering()}>
        <input
          type="text"
          class={styles.filterInput}
          placeholder="Filter…"
          aria-label={`Filter ${props.ariaLabel}`}
          data-testid="loading-menu-filter"
          ref={filterEl}
          value={query()}
          onInput={e => setQuery(e.currentTarget.value)}
        />
      </Show>
      {/* The filtered form renders `as="div"`, so the popover is a plain
          element with no menu semantics of its own -- and ARIA requires a
          `menuitemradio` to be owned by a `menu`, `menubar` or `group`.
          Without this wrapper a screen reader announced the branch picker (the
          one caller that filters, and the longest list in the app) as a set of
          orphaned radios with no group name and no set position.

          Wrapped HERE and not on the popover, because `as="div"` serves two
          different callers: this one, whose items ARE the popover's children,
          and a panel like `FilesSortMenu` that supplies its own inner
          `role="menu"` around several groups. A role on the popover would give
          that panel two nested menus. The unfiltered form needs none of this --
          `DropdownMenu` renders a real `<menu>` there. */}
      <Show when={filtering()} fallback={items()}>
        <div role="menu" aria-label={props.ariaLabel} class={styles.filteredList}>{items()}</div>
      </Show>
    </DropdownMenu>
  )
}
