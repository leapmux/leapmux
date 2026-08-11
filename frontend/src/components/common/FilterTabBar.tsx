import type { JSX } from 'solid-js'
import { For, onCleanup } from 'solid-js'
import * as styles from './FilterTabBar.css'

/** One tab: the value it selects, and the text it shows. */
export interface FilterTab<K extends string> {
  key: K
  label: string
}

export interface FilterTabBarProps<K extends string> {
  /** The tabs, in render order. */
  tabs: readonly FilterTab<K>[]
  /** The selected tab's key. */
  active: K
  /** Invoked with the newly selected key. */
  onSelect: (key: K) => void
  /** Accessible name for the tab list, e.g. "Filter files". */
  ariaLabel: string
  /** id of the region the tabs swap, for aria-controls. */
  panelId: string
  /** data-testid on the tab list. */
  testId?: string
  /** data-testid for one tab. Called per key. */
  tabTestId?: (key: K) => string
}

/**
 * Which tab an arrow/Home/End keypress moves to, or undefined for a key the tab
 * bar does not own.
 *
 * Pure and exported so the contract role=tab requires (Tab reaches the SET, the
 * arrows move within it, and both ends wrap) is testable on its own.
 */
export function nextFilterTab<K extends string>(keys: readonly K[], current: K, key: string): K | undefined {
  if (keys.length === 0)
    return undefined
  const i = keys.indexOf(current)
  const cur = i < 0 ? 0 : i
  switch (key) {
    case 'ArrowRight':
    case 'ArrowDown':
      return keys[(cur + 1) % keys.length]
    case 'ArrowLeft':
    case 'ArrowUp':
      return keys[(cur - 1 + keys.length) % keys.length]
    case 'Home':
      return keys[0]
    case 'End':
      return keys[keys.length - 1]
    default:
      return undefined
  }
}

/**
 * A row of filter tabs over one region.
 *
 * A real TAB SET, not a row of toggle buttons: picking a tab swaps the content
 * of the region below it, which is what role=tab describes. A toggle button
 * announces "pressed", promises it can be un-pressed (clicking the active
 * filter does nothing), and carries no group name and no "2 of 4". role=tab
 * announces "Changed, tab, selected, 2 of 4" inside a named tab list.
 *
 * Roving tabindex + arrow keys come with the role -- APG requires Tab to reach
 * the SET and the arrows to move within it, so only the selected tab is in the
 * tab order and focus follows the selection.
 */
export function FilterTabBar<K extends string>(props: FilterTabBarProps<K>): JSX.Element {
  // Focus has to follow selection for the roving tabindex to work, hence the
  // element refs: the tab that leaves the tab order must hand focus over.
  const tabEls = new Map<K, HTMLButtonElement>()

  const selectTab = (key: K) => {
    props.onSelect(key)
    tabEls.get(key)?.focus()
  }

  const onKeyDown = (e: KeyboardEvent) => {
    const next = nextFilterTab(props.tabs.map(t => t.key), props.active, e.key)
    if (next === undefined)
      return
    e.preventDefault()
    selectTab(next)
  }

  return (
    <div
      class={styles.tabBar}
      role="tablist"
      aria-label={props.ariaLabel}
      data-testid={props.testId}
      onKeyDown={onKeyDown}
    >
      <For each={props.tabs}>
        {tab => (
          <button
            type="button"
            class={styles.tabButton}
            classList={{ [styles.tabButtonActive]: props.active === tab.key }}
            role="tab"
            aria-selected={props.active === tab.key}
            aria-controls={props.panelId}
            tabIndex={props.active === tab.key ? 0 : -1}
            ref={(el) => {
              tabEls.set(tab.key, el)
              // Solid does not re-invoke a ref with null on disposal, so a
              // caller whose `tabs` shrink would otherwise leave a detached
              // button in the map -- and `selectTab` would move focus to it,
              // which lands on `<body>` and takes the arrow keys with it. The
              // identity check keeps a remove-then-re-add of the same key from
              // deleting the NEW element's entry.
              onCleanup(() => {
                if (tabEls.get(tab.key) === el)
                  tabEls.delete(tab.key)
              })
            }}
            // `selectTab`, not `props.onSelect` alone: the roving tabindex moves
            // to this tab, so focus has to move with it. WebKit does not focus a
            // button on click, which would otherwise leave `tabIndex={0}` here
            // while focus sits on `<body>` -- and the arrow keys, which the
            // tablist handles, would then reach nothing after a mouse selection.
            onClick={() => selectTab(tab.key)}
            data-testid={props.tabTestId?.(tab.key)}
          >
            {tab.label}
          </button>
        )}
      </For>
    </div>
  )
}
