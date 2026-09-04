import type { Component, JSX } from 'solid-js'
import type { NavGroup } from './navGroups'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, For, Show } from 'solid-js'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { createKeyedElementRefs } from '~/lib/keyedElementRefs'
import { nextRovingValue } from '~/lib/rovingFocus'
import { menuSectionHeader } from '~/styles/shared.css'
import * as styles from './PreferencesDialog.css'

export interface PreferencesNavProps {
  groups: readonly NavGroup[]
  /**
   * The RESOLVED active group, not its id. The dialog already resolves the
   * deep-link id against the visible groups (an id whose group is hidden
   * falls back to the first one), so taking the id here made the nav
   * re-derive the same rule over the same list — two statements of one
   * fallback, either of which could change alone.
   */
  active: NavGroup
  onSelect: (id: string) => void
  /** Marker data: nav ids whose group holds at least one restart setting. */
  restartGroups: () => ReadonlySet<string>
  /**
   * Below `sm`: collapse the sidebar tab list into an oat-styled dropdown.
   * A swipeable horizontal tab bar is not usable on a phone with this many
   * sections; a native `<select>` would open the OS picker and ignore the
   * theme, so we use the same DropdownMenu pattern as AgentProviderSelector.
   */
  compact: boolean
}

/** Option label: append the same warning mark the sidebar tabs show. */
function optionLabel(group: NavGroup, restart: boolean): string {
  return restart ? `${group.title} \u26A0` : group.title
}

/**
 * The dialog's category navigation.
 *
 * Desktop: a real tab list (role=tablist, roving tabindex, arrow keys via
 * `nextRovingValue`), with labelled PREFERENCES and ADMINISTRATION dividers.
 *
 * Compact (phone): an oat-styled DropdownMenu with matching section headers —
 * one tap opens an in-app menu instead of the OS native picker.
 */
export const PreferencesNav: Component<PreferencesNavProps> = (props) => {
  const ids = () => props.groups.map(g => g.id)
  const userGroups = createMemo(() => props.groups.filter(g => !g.admin))
  const adminGroups = createMemo(() => props.groups.filter(g => g.admin))

  const sectionItems = (groups: readonly NavGroup[]): JSX.Element => (
    <For each={groups}>
      {group => (
        <DropdownMenuCheckableItem
          kind="radio"
          label={optionLabel(group, props.restartGroups().has(group.id))}
          checked={props.active.id === group.id}
          data-testid={`preferences-nav-${group.id}`}
          onSelect={() => props.onSelect(group.id)}
        />
      )}
    </For>
  )

  const compactSelect = (): JSX.Element => (
    <DropdownMenu
      class={styles.navMenu}
      aria-label="Preferences sections"
      data-testid="preferences-nav-menu"
      trigger={triggerProps => (
        <button
          type="button"
          class={styles.navSelect}
          aria-label="Preferences sections"
          aria-expanded={triggerProps['aria-expanded']}
          data-testid="preferences-nav"
          ref={triggerProps.ref}
          onPointerDown={triggerProps.onPointerDown}
          onClick={triggerProps.onClick}
        >
          <span class={styles.navSelectValue}>
            {optionLabel(props.active, props.restartGroups().has(props.active.id))}
          </span>
          <ChevronDown size={16} class={styles.navSelectChevron} aria-hidden="true" />
        </button>
      )}
    >
      <Show when={userGroups().length > 0}>
        <li class={menuSectionHeader}>PREFERENCES</li>
        {sectionItems(userGroups())}
      </Show>
      <Show when={adminGroups().length > 0}>
        <li class={menuSectionHeader}>ADMINISTRATION</li>
        {sectionItems(adminGroups())}
      </Show>
    </DropdownMenu>
  )

  const tabEls = createKeyedElementRefs<string, HTMLButtonElement>()
  const select = (id: string) => {
    props.onSelect(id)
    tabEls.get(id)?.focus()
  }
  const onKeyDown = (e: KeyboardEvent) => {
    const next = nextRovingValue(ids(), props.active.id, e)
    if (next === undefined)
      return
    e.preventDefault()
    select(next.value)
  }

  return (
    <Show when={!props.compact} fallback={compactSelect()}>
      <nav role="tablist" aria-label="Preferences sections" class={styles.nav} onKeyDown={onKeyDown}>
        <Show when={userGroups().length > 0}>
          <div class={styles.navDivider} role="separator" aria-label="Preferences">PREFERENCES</div>
        </Show>
        <For each={props.groups}>
          {(group, i) => (
            <>
              <Show when={group.admin && !props.groups[i() - 1]?.admin}>
                {/* The rule divides the two halves, so it needs both of them.
                  With no user categories ADMINISTRATION leads the list, and a
                  rule above it would hang under the search box dividing
                  nothing. It is decorative either way: the header below is
                  already the labelled separator, and a bare <hr> would
                  announce a second, unlabelled one inside the tab list. */}
                <Show when={userGroups().length > 0}>
                  <hr class={styles.navSeparator} aria-hidden="true" />
                </Show>
                <div class={styles.navDivider} role="separator" aria-label="Administration">ADMINISTRATION</div>
              </Show>
              <button
                type="button"
                role="tab"
                class={styles.navButton}
                classList={{ [styles.navButtonActive]: props.active.id === group.id }}
                aria-selected={props.active.id === group.id}
                aria-controls="preferences-panel"
                tabIndex={props.active.id === group.id ? 0 : -1}
                data-testid={`preferences-nav-${group.id}`}
                ref={el => tabEls.register(group.id, el)}
                onClick={() => select(group.id)}
              >
                {group.title}
                <Show when={props.restartGroups().has(group.id)}>
                  {/* Decorative marker; each row's Requires Restart badge carries the
                    accessible statement. */}
                  <span aria-hidden="true">&#x26A0;</span>
                </Show>
              </button>
            </>
          )}
        </For>
      </nav>
    </Show>
  )
}
