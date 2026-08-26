import type { Component } from 'solid-js'
import type { SearchEntry } from './search'
import { createEffect, createMemo, createSignal, For, on, onMount, Show, untrack } from 'solid-js'
import { Alert } from '~/components/common/Alert'
import { Dialog } from '~/components/common/Dialog'
import { useAuth } from '~/context/AuthContext'
import { usePreferences } from '~/context/PreferencesContext'
import { useViewportBelow } from '~/hooks/useViewportBelow'
import { createAdminSettingsStore } from '~/stores/adminSettings.store'
import { breakpoints } from '~/styles/tokens'
import { NAV_GROUPS } from './navGroups'
import { groupRowsByNav, navIdsWhere, occupiedNavGroups } from './navRows'
import * as styles from './PreferencesDialog.css'
import { PreferencesNav } from './PreferencesNav'
import { PreferencesSearch } from './PreferencesSearch'
import { buildProtoRows } from './protoRegistry'
import { createBrowserRows } from './registry'
import { breadcrumb, buildSearchIndex, matchSettings } from './search'
import { SettingsPanel } from './SettingsPanel'

interface PreferencesDialogProps {
  /**
   * The category id to open on (a NAV_GROUPS id). An id whose group is hidden
   * — Account in solo mode — falls back to the first visible one, so a caller
   * never has to know which sections this deployment has.
   */
  category: string
  /**
   * How many times a caller asked for this dialog. See `openPreferences`:
   * the count changes on every request, where the category does not.
   */
  openSeq: number
  onClose: () => void
}

export const PreferencesDialog: Component<PreferencesDialogProps> = (props) => {
  const auth = useAuth()
  const prefs = usePreferences()
  const narrow = useViewportBelow(breakpoints.sm)

  const isAdmin = () => auth.user()?.isAdmin === true
  const adminStore = createAdminSettingsStore(isAdmin)

  // A MEMO, not a one-time call: an account-backed entry takes its shape
  // from the hub's descriptor, so the row set changes once, when the reply
  // lands. Inside one build, descriptor and binding come from the same
  // call, so a visibility rule that reads a preference is resolved against
  // the context that the binding writes to.
  const browserRows = createMemo(() => createBrowserRows(prefs, prefs.accountDescriptors()))

  const [selected, setSelected] = createSignal(untrack(() => props.category))
  // A deep link may re-fire while the dialog is open (the app menu, or the
  // shortcut, while it is visible); follow it. `openSeq` is tracked BESIDE
  // the category because every entry point asks for the same section: a
  // repeat request writes the same string, which notifies nothing, so the
  // category alone left the dialog on whatever section the user last picked.
  createEffect(on(
    () => [props.category, props.openSeq] as const,
    ([category]) => setSelected(category),
  ))

  // `buildProtoRows` serves the HUB scope alone. An ACCOUNT key's row also
  // starts from a wire descriptor, but its binding is the preferences
  // context's typed, browser-overridable one rather than a merge onto a
  // store's value map, so the registry builds those — see
  // `createBrowserRows`.
  const adminRows = createMemo(() => {
    if (!isAdmin())
      return []
    return buildProtoRows(adminStore.state.descriptors, adminStore)
  })

  /**
   * Every visible row, in the navigation group that renders it.
   *
   * The ONE derivation the navigation, the panel, the restart marks and the
   * search index all read. Five memos re-derived the same membership rule
   * before, each branching on `group.admin` again — and each of them took
   * the USER half of that branch to be empty, so a restart-class row in a
   * user group marked neither the navigation nor the panel. The rows a
   * group holds are now one answer, whichever source they came from.
   */
  const rowsByGroup = createMemo(() =>
    groupRowsByNav(NAV_GROUPS, {
      isAdmin: isAdmin(),
      browserRows: browserRows(),
      adminRows: adminRows(),
    }),
  )

  const visibleGroups = createMemo(() => occupiedNavGroups(NAV_GROUPS, rowsByGroup()))
  const activeGroup = createMemo(() =>
    visibleGroups().find(g => g.id === selected()) ?? visibleGroups()[0],
  )

  /** Visible rows for the active group, the ONE row set the panel renders. */
  const activeRows = createMemo(() => rowsByGroup().get(activeGroup()?.id ?? '') ?? [])

  const [query, setQuery] = createSignal('')
  const searching = () => query().trim() !== ''

  /**
   * The nav ids whose group holds at least one VISIBLE restart-class row.
   *
   * Derived from the same rows the panel's own warning is derived from: a
   * group whose restart rows are all hidden has nothing in it that needs a
   * restart, so marking it would be false. A group with no rows at all
   * cannot hold one, so the occupancy test is not repeated here.
   *
   * `elevationGroups` beneath answers the same question for the elevation
   * window, from the same rows, through the same helper.
   */
  const restartGroups = createMemo(() => navIdsWhere(rowsByGroup(), row => row.descriptor.restart === true))

  /**
   * The nav ids whose group holds at least one VISIBLE row the hub refuses
   * without a recently proven factor.
   *
   * The sibling of `restartGroups`, from the same rows and by the same rule,
   * so the two marks cannot be derived differently. The panel shows the
   * verified-session state at the top of these groups.
   */
  const elevationGroups = createMemo(() => navIdsWhere(rowsByGroup(), row => row.descriptor.needsElevation === true))

  /**
   * Both row sources join one search index, built from the SAME rows the
   * panels render. A hand-written second derivation drifted from
   * `buildProtoRows`: it re-implemented the id scheme and applied only
   * half the visibility rules, so a hidden field stayed searchable and its
   * result jumped to a panel that does not show it.
   */
  const searchEntries = createMemo<SearchEntry[]>(() => {
    const byGroup = rowsByGroup()
    return NAV_GROUPS.flatMap(group =>
      (byGroup.get(group.id) ?? []).map(({ descriptor }) => ({
        groupTitle: group.title,
        navId: group.id,
        label: descriptor.label,
        help: descriptor.help,
        keywords: descriptor.keywords,
        optionLabels: descriptor.control.kind === 'enum'
          ? descriptor.control.options.map(o => o.label)
          : undefined,
      })),
    )
  })

  const searchIndex = createMemo(() => buildSearchIndex(searchEntries()))

  const results = createMemo(() =>
    matchSettings(searchIndex(), query(), visibleGroups().map(g => g.id)),
  )

  onMount(() => {
    if (isAdmin())
      void adminStore.load()
    // RETRY a failed account load, here, where its rows are about to be
    // rendered. The context loads once at PROVIDER mount, and
    // `usePreferencesForIdentity` covers the one failure that
    // has an event to hang off: the load that answered Unauthenticated
    // because the visitor signed in later in the same page.
    //
    // Every OTHER failure has none. An unreachable hub, a timeout, or a 500
    // leaves the account rows absent for the rest of the session, because
    // the identity never changes and nothing else asks again. Opening this
    // dialog is where those rows are wanted, so it is where the retry
    // belongs — without it the dialog stays short of the Appearance and
    // Keyboard Shortcuts groups until the user reloads the page.
    //
    // Only after a FAILURE: a load that succeeded described a key set that
    // does not change while the page is open.
    if (prefs.accountLoadError() !== null) {
      // `reload` records its own failure in `accountLoadError`, which the
      // banner beneath already renders, so a second failure needs nothing
      // here but a handled rejection.
      void prefs.reload().catch(() => {})
    }
  })

  return (
    <Dialog title="Preferences" huge onClose={props.onClose}>
      <div class={styles.layout}>
        <div class={styles.navColumn}>
          <PreferencesSearch query={query()} onQuery={setQuery} />
          {/* `activeGroup()` is undefined only while the group list is
            empty — a non-admin whose registry somehow yields no visible
            row. The nav takes the RESOLVED group, so the guard is what
            makes that prop non-optional. */}
          <Show when={!searching() && activeGroup()}>
            {group => (
              <PreferencesNav
                groups={visibleGroups()}
                active={group()}
                onSelect={setSelected}
                restartGroups={restartGroups}
                compact={narrow()}
              />
            )}
          </Show>
        </div>
        <div class={styles.panelColumn}>
          {/*
            The two scopes state their failures APART, because the rows each
            one loses differ and a merged banner could name only one of
            them. Both are silent otherwise, and indistinguishable from a
            legitimate absence: a failed `ListSettings` leaves zero admin
            descriptors, `occupiedNavGroups` drops every ADMINISTRATION
            group, and the dialog reads exactly like a non-admin session.
          */}
          <Show when={adminStore.state.error}>
            {message => <Alert variant="error">{message()}</Alert>}
          </Show>
          {/*
            The hub declares every account key's shape, so a load that fails
            leaves this dialog with no account row to build — not a row at a
            default this client invented. Say which rows are missing and how
            to get them back, because "Theme" simply not being there reads
            as a broken app rather than a failed request.
          */}
          <Show when={prefs.accountLoadError()}>
            {message => (
              <Alert variant="error">
                {message()}
                <div>Settings saved to your account are not listed. Reload the page to try again.</div>
              </Alert>
            )}
          </Show>
          <Show
            when={!searching()}
            fallback={(
              <div class={styles.searchResults} data-testid="preferences-search-results">
                <For each={results()}>
                  {group => (
                    <>
                      <div class={styles.searchGroupTitle}>{group.groupTitle}</div>
                      <For each={group.entries}>
                        {entry => (
                          <button
                            type="button"
                            class={styles.searchResultButton}
                            onClick={() => {
                              setSelected(entry.navId)
                              setQuery('')
                            }}
                          >
                            {breadcrumb(entry)}
                            <Show when={entry.help}>
                              <span class={styles.searchResultHelp}>{entry.help}</span>
                            </Show>
                          </button>
                        )}
                      </For>
                    </>
                  )}
                </For>
                <Show when={results().length === 0}>
                  <div class={styles.searchResultHelp}>
                    No settings match “
                    {query().trim()}
                    ”.
                  </div>
                </Show>
              </div>
            )}
          >
            <SettingsPanel
              rows={activeRows()}
              restartGroup={restartGroups().has(activeGroup()?.id ?? '')}
              elevationGroup={elevationGroups().has(activeGroup()?.id ?? '')}
              writeError={activeGroup()?.admin ? adminStore.state.writeError : null}
            />
          </Show>
        </div>
      </div>
    </Dialog>
  )
}
