import type { Component, JSX } from 'solid-js'
import type { TileActions } from './TileActionsMenu'
import type { TabPopAction } from '~/components/common/TabContextMenu'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import type { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { GuardedDragRow } from '~/lib/dragRow'
import type { Tab } from '~/stores/tab.types'
import { createDroppable, SortableProvider } from '@thisbeyond/solid-dnd'
import Bot from 'lucide-solid/icons/bot'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import Ellipsis from 'lucide-solid/icons/ellipsis'
import Menu from 'lucide-solid/icons/menu'
import PanelRight from 'lucide-solid/icons/panel-right'
import Plus from 'lucide-solid/icons/plus'
import Terminal from 'lucide-solid/icons/terminal'
import X from 'lucide-solid/icons/x'
import { createEffect, createSignal, ErrorBoundary, For, onCleanup, onMount, Show } from 'solid-js'
import { AgentProviderIcon, agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { DragHandle } from '~/components/common/DragHandle'
import { createContextMenuAnchor, DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { NewTabMenuItems } from '~/components/common/NewTabMenuItems'
import { providerButton } from '~/components/common/NewTabMenuItems.css'
import { TabContextMenu } from '~/components/common/TabContextMenu'
import { TabTypeIcon } from '~/components/common/TabTypeIcon'
import { Tooltip } from '~/components/common/Tooltip'
import { usePreferences } from '~/context/PreferencesContext'
import { TAB_TYPE_WIRE_TOKEN } from '~/generated/contracts/tab-types'
import { useMruProviders } from '~/hooks/useMruProviders'
import { attachDragActivators } from '~/lib/dragActivators'
import { createGuardedSortableRow } from '~/lib/dragRow'
import { createKeyedRows, KeyedFor } from '~/lib/keyedRows'
import { shortcutHint } from '~/lib/shortcuts/display'
import { canCloseTab, canRenameTab, tabDisplayLabel, tabKey, tabTooltipShowWhen, tabTooltipText, terminalProgressBarProps, terminalProgressVisible } from '~/stores/tab.helpers'
import { isTerminalTab } from '~/stores/tab.types'
import { menuSectionHeader } from '~/styles/shared.css'
import * as styles from './TabBar.css'
import { TABBAR_ZONE_PREFIX, useTabDrag } from './TabDragContext'
import { TabSheet } from './TabSheet'
import { terminalStatusClassList } from './terminalStatus'
import { TileActionsMenu } from './TileActionsMenu'
import { UserMenuItems } from './UserMenuItems'

const TabBarTooltip: Component<{ text: string, children: JSX.Element }> = tipProps => (
  <Tooltip text={tipProps.text}>
    <span class={styles.tooltipTrigger}>
      {tipProps.children}
    </span>
  </Tooltip>
)

const TabTextWithTooltip: Component<{ label: string, tooltip: string, showWhen: 'always' | 'clipped', status?: TerminalStatus }> = (props) => {
  return (
    <Tooltip text={props.tooltip} showWhen={props.showWhen}>
      <span
        class={styles.tabText}
        classList={terminalStatusClassList(props.status)}
      >
        {props.label}
      </span>
    </Tooltip>
  )
}

/**
 * A terminal tab's status, or `undefined` for the other kinds.
 *
 * Takes the tab by VALUE so `isTerminalTab` can narrow it: the rows read their
 * tab through an accessor now, and two calls to that accessor are two separate
 * expressions, so a guard on the first cannot narrow the second.
 */
function terminalStatusOf(tab: Tab): TerminalStatus | undefined {
  return isTerminalTab(tab) ? tab.status : undefined
}

/**
 * The token this row publishes as `data-tab-type`.
 *
 * GENERATED from contracts/tab-types.json, shared with the Go side that spells
 * the same tokens on the command line. It was a hand-written switch, and a kind
 * missing from it does not fail to compile -- it publishes a token no E2E
 * locator matches, which reads as a mystified selector rather than an error.
 */
function tabTypeLabel(type: TabType): string {
  return TAB_TYPE_WIRE_TOKEN[type] || 'unknown'
}

/** New-tab actions surfaced by the Plus button and overflow menu. */
interface TabBarNewTabProps {
  showAddButton: boolean
  onNewAgent: (provider?: AgentProvider) => void
  onNewTerminal: () => void
  onNewTerminalWithShell?: (shell: string) => void
  onNewAgentAdvanced?: () => void
  onNewTerminalAdvanced?: () => void
  availableProviders?: AgentProvider[]
  availableShells?: string[]
  defaultShell?: string
  newAgentLoadingProvider?: AgentProvider | null
  newTerminalLoading?: boolean
  newShellLoading?: boolean
  hasActiveTabContext?: boolean
}

/**
 * Mobile chrome rendered in the tab bar. The presence of the prop bundle
 * itself signals "we're in mobile-layout mode" — pass `undefined` (or omit)
 * on desktop. Every field is required, so a partial bundle is a type error
 * rather than mobile chrome that silently does nothing.
 */
interface TabBarMobileProps {
  /** Whether the tab sheet is open — the shell's overlay state owns this. */
  sheetOpen: () => boolean
  onToggleDrawer: (side: 'left' | 'right') => void
  onToggleSheet: () => void
  onCloseSheet: () => void
}

interface TabBarProps {
  tileId: string
  tabs: Tab[]
  activeTabKey: string | null
  /**
   * The workspace these tabs belong to is archived, so the strip offers no
   * mutation. It offers no close, rename, or middle-click close.
   *
   * Was `readOnly`. Archival is the only thing that blocks mutation, so the two
   * were one concept under two names; the sidebar tree carries the same flag.
   */
  archived?: boolean
  closingTabKeys?: Set<string>
  isEditingRef?: (fn: () => boolean) => void
  onSelect: (tab: Tab) => void
  onClose: (tab: Tab) => void
  onRename: (tab: Tab, title: string) => void
  /** New-tab plumbing (loading flags, shell list, advanced dialog hooks). */
  newTab: TabBarNewTabProps
  /** Mobile chrome; omit on desktop. */
  mobile?: TabBarMobileProps
  /** Tile-level actions in the overflow menu. */
  tileActions?: TileActions
  /**
   * The pop-out / pop-in affordance for one tab, for that tab's context menu.
   * Built by `TileRenderer` from the same function that builds the tile-level one.
   */
  tabPop?: (tab: Tab) => TabPopAction | undefined
}

export const TabBar: Component<TabBarProps> = (props) => {
  const prefs = usePreferences()
  const { mruProviders, recordProviderUse } = useMruProviders(() => props.newTab.availableProviders ?? [], 2)
  const handleNewAgent = (provider?: AgentProvider) => {
    if (provider !== undefined)
      recordProviderUse(provider)
    props.newTab.onNewAgent(provider)
  }
  const newTerminalLabel = () => props.newTab.hasActiveTabContext ? 'New terminal at the current working directory' : 'New terminal...'

  const [editingTabKey, setEditingTabKey] = createSignal<string | null>(null)
  const [editingValue, setEditingValue] = createSignal('')

  // Cross-tile drag context (may not be available on mobile single-tile layout)
  let crossTileDrag: ReturnType<typeof useTabDrag> | undefined
  try {
    crossTileDrag = useTabDrag()
  }
  catch { /* not wrapped in provider */ }

  const isDropTarget = () => {
    if (!crossTileDrag)
      return false
    const overTile = crossTileDrag.dragOverTileId()
    const srcTile = crossTileDrag.dragSourceTileId()
    return overTile === props.tileId && srcTile !== props.tileId
  }

  // Expose editing state to parent so it can avoid stealing focus during rename.
  // This is intentionally called once during setup (not reactive).
  // eslint-disable-next-line solid/reactivity
  props.isEditingRef?.(() => editingTabKey() !== null)

  let editCancelled = false
  let tabListRef: HTMLDivElement | undefined

  /** The tab the mobile chip speaks for: the active one, falling back to the first. */
  const chipTab = () => props.tabs.find(t => tabKey(t) === props.activeTabKey) ?? props.tabs[0]

  const startEditing = (tab: Tab) => {
    setEditingTabKey(tabKey(tab))
    setEditingValue(tabDisplayLabel(tab))
  }

  const commitEdit = (tab: Tab) => {
    if (editCancelled) {
      editCancelled = false
      return
    }
    const value = editingValue().trim()
    if (value && value !== tabDisplayLabel(tab)) {
      props.onRename(tab, value)
    }
    setEditingTabKey(null)
  }

  const cancelEdit = () => {
    editCancelled = true
    setEditingTabKey(null)
  }

  /**
   * Rows keyed on TAB KEYS, not on the `Tab` objects -- see {@link createKeyedRows}.
   *
   * This strip is where that matters most: a row holds the inline rename
   * `<input>`, so remounting it mid-rename destroys the element the user is
   * typing into, and it holds `createSortable`'s drag handle.
   */
  const { keys: ids, byKey: tabByKey } = createKeyedRows(() => props.tabs, tabKey)

  const handleTabChange = (value: string) => {
    const tab = props.tabs.find(t => tabKey(t) === value)
    if (tab)
      props.onSelect(tab)
  }

  const handleWheel = (e: WheelEvent) => {
    if (!tabListRef || Math.abs(e.deltaY) < Math.abs(e.deltaX))
      return
    const canScrollHorizontally = tabListRef.scrollWidth > tabListRef.clientWidth
    if (!canScrollHorizontally)
      return
    e.preventDefault()
    tabListRef.scrollLeft += e.deltaY
  }

  onMount(() => {
    tabListRef?.addEventListener('wheel', handleWheel, { passive: false })
    onCleanup(() => tabListRef?.removeEventListener('wheel', handleWheel))
  })

  // Zone droppable for cross-tile drops (drop target for the whole tab bar area)
  // May fail if DragDropProvider context isn't available (e.g. during rapid tab creation)
  let zoneDroppable: ReturnType<typeof createDroppable> | undefined
  try {
    // eslint-disable-next-line solid/reactivity -- stable identifier for createDroppable
    zoneDroppable = createDroppable(`${TABBAR_ZONE_PREFIX}${props.tileId}`)
  }
  catch { /* DragDropProvider context not available */ }

  /** The inline rename input, shared by the strip row and the sheet row. */
  const TabRenameInput: Component<{ tab: () => Tab }> = renameProps => (
    <input
      class={styles.tabEditInput}
      data-testid="tab-rename-input"
      type="text"
      value={editingValue()}
      onInput={e => setEditingValue(e.currentTarget.value)}
      onKeyDown={(e) => {
        e.stopPropagation()
        if (e.key === 'Enter') {
          e.preventDefault()
          commitEdit(renameProps.tab())
        }
        else if (e.key === 'Escape') {
          cancelEdit()
        }
      }}
      onBlur={() => commitEdit(renameProps.tab())}
      onClick={e => e.stopPropagation()}
      ref={(el) => {
        requestAnimationFrame(() => {
          el.focus()
          el.select()
        })
      }}
    />
  )

  /**
   * Per-row DnD wiring, created by `rowSetup` in the FOR-ROW owner — outside
   * the `<Show>` — because the guarded body activators are a `createEffect`
   * and an effect created inside the Show's children evaluation is torn down
   * by a lookup flicker, mid-gesture. (The grip's activators live in the
   * `DragHandle` component, which owns its own scope and is safe.)
   *
   * The drag row itself comes from the guarded factory (`~/lib/dragRow.ts`),
   * which owns the activation-split protocol; the context-menu anchor is
   * created unconditionally so the ErrorBoundary fallbacks keep their menus
   * even when the sortable context is unavailable.
   */
  interface TabRowDnd {
    /** The guarded drag row: node-only ref, split activators, transform style. */
    row: GuardedDragRow
    /** Row-element anchor: the context menu and the guarded body activators. */
    rowEl: () => HTMLElement | undefined
    setRowEl: (el: HTMLElement) => void
  }

  const setupTabRowDnd = (key: string): TabRowDnd => {
    const [rowEl, setRowEl] = createContextMenuAnchor()
    // The key is the row's identity for the whole of its life now, so the
    // sortable id is fixed at creation rather than re-derived from a `Tab`
    // the row no longer owns. Created HERE, in the for-row owner, so it
    // outlives a tick where the lookup misses.
    const row = createGuardedSortableRow(key)
    // Mouse-only activation on the row body; the grip carries the raw
    // handlers, so touch drags start there and nowhere else.
    attachDragActivators(rowEl, row.bodyActivators, { touch: 'block' })
    return { row, rowEl, setRowEl }
  }

  /** How one row differs between the desktop strip and the mobile sheet. */
  interface TabRowSurface {
    rowClass: string
    rowTestId: string
    gripTestId: string
    /** The sheet shows its grip standing; the strip hides it under a fine pointer. */
    gripVisibility: 'auto' | 'always'
    menuTestId: string
    /** The label, with its surface-specific clipping and tooltip. */
    renderLabel: () => JSX.Element
    /** What a click or Enter does: select the tab, and on the sheet close it too. */
    activate: () => void
    /** The strip's mouse conveniences; the sheet has none of them. */
    onAuxClick?: (e: MouseEvent) => void
    onDblClick?: (e: MouseEvent) => void
  }

  // `tab` is an ACCESSOR, not a value: the row outlives any single `Tab` object
  // now that the `<For>` keys on `ids()`, so every field has to be read through
  // it to stay live. Reading it once here would freeze the row at whatever the
  // tab looked like when it mounted.
  const renderTabRow = (tab: () => Tab, dnd: TabRowDnd | undefined, surface: TabRowSurface) => {
    const row = dnd?.row
    const isClosing = () => props.closingTabKeys?.has(tabKey(tab())) ?? false
    const canRename = () => canRenameTab(props.archived, tab())
    // Suppress the click that fires on pointerup after a drag: dropping a row
    // back on itself must not also select it (and on the sheet, close it).
    let wasDragging = false
    createEffect(() => {
      if (row?.isActiveDraggable)
        wasDragging = true
    })
    return (
      <div
        role="tab"
        ref={(el) => {
          dnd?.setRowEl(el)
          // Node registration only — activation is on the guarded body and
          // the grip above, never attached wholesale: a touch press on the
          // row must not reach the drag sensor at all.
          row?.ref(el)
        }}
        tabIndex={0}
        aria-selected={props.activeTabKey === tabKey(tab())}
        class={surface.rowClass}
        classList={{ [styles.tabDragging]: row?.isActiveDraggable }}
        style={dnd ? dnd.row.style() : undefined}
        data-testid={surface.rowTestId}
        data-tab-type={tabTypeLabel(tab().type)}
        data-tab-id={tab().id}
        data-terminal-status={terminalStatusOf(tab())}
        onClick={() => {
          if (wasDragging) {
            wasDragging = false
            return
          }
          surface.activate()
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            surface.activate()
          }
        }}
        onAuxClick={surface.onAuxClick}
        onDblClick={surface.onDblClick}
      >
        <DragHandle visibility={surface.gripVisibility} activators={() => row?.gripActivators()} testId={surface.gripTestId} />
        <span class={styles.tabIcon}>
          <TabTypeIcon tab={tab()} />
        </span>
        <Show
          when={editingTabKey() === tabKey(tab())}
          fallback={surface.renderLabel()}
        >
          <TabRenameInput tab={tab} />
        </Show>
        <Show when={tab().hasNotification}>
          <span class={styles.tabNotification} data-testid="tab-notification" />
        </Show>
        <Show when={terminalProgressVisible(tab())}>
          <span
            class={styles.tabProgress}
            data-testid="tab-progress"
            {...terminalProgressBarProps(tab())}
          />
        </Show>
        <Show when={canCloseTab(props.archived, tab())}>
          <IconButton
            icon={X}
            class={styles.tabClose}
            state={isClosing() ? IconButtonState.Loading : IconButtonState.Enabled}
            data-testid="tab-close"
            onPointerDown={e => e.stopPropagation()}
            onClick={(e) => {
              e.stopPropagation()
              if (isClosing())
                return
              props.onClose(tab())
            }}
          />
        </Show>
        {/* Outside the close block: a tab that cannot be closed can still be
            renamed or popped out. The menu host collapses to `display: contents`,
            so it costs the row no layout either way. */}
        <TabContextMenu
          contextMenuFor={dnd?.rowEl}
          data-testid={surface.menuTestId}
          onRename={canRename() ? () => startEditing(tab()) : undefined}
          onClose={canCloseTab(props.archived, tab()) ? () => props.onClose(tab()) : undefined}
          isClosing={isClosing()}
          pop={props.tabPop?.(tab())}
        />
      </div>
    )
  }

  /** A row of the desktop strip. */
  const renderStripRow = (tab: () => Tab, dnd?: TabRowDnd) => renderTabRow(tab, dnd, {
    rowClass: styles.tab,
    rowTestId: 'tab',
    gripTestId: 'tab-drag-handle',
    gripVisibility: 'auto',
    menuTestId: 'tab-bar-tab-menu',
    renderLabel: () => (
      <TabTextWithTooltip
        label={tabDisplayLabel(tab())}
        tooltip={tabTooltipText(tab())}
        showWhen={tabTooltipShowWhen(tab())}
        status={terminalStatusOf(tab())}
      />
    ),
    activate: () => handleTabChange(tabKey(tab())),
    onAuxClick: (e: MouseEvent) => {
      if (e.button === 1) {
        e.preventDefault()
        if (!canCloseTab(props.archived, tab()) || props.closingTabKeys?.has(tabKey(tab())))
          return
        props.onClose(tab())
      }
    },
    onDblClick: (e: MouseEvent) => {
      e.preventDefault()
      e.stopPropagation()
      if (canRenameTab(props.archived, tab()))
        startEditing(tab())
    },
  })

  /** A row of the mobile tab sheet. */
  const renderSheetRow = (tab: () => Tab, dnd?: TabRowDnd) => renderTabRow(tab, dnd, {
    rowClass: styles.sheetRow,
    rowTestId: 'tab-sheet-row',
    gripTestId: 'tab-sheet-drag-handle',
    gripVisibility: 'always',
    menuTestId: 'tab-sheet-row-menu',
    renderLabel: () => <span class={styles.sheetRowLabel}>{tabDisplayLabel(tab())}</span>,
    activate: () => {
      handleTabChange(tabKey(tab()))
      props.mobile?.onCloseSheet()
    },
  })

  // Shared menu items for "More options" (used in full, collapsed-new-tab, and collapsed-overflow menus)
  const renderMoreMenuItems = () => (
    <>
      {/* `shortcuts`: these items act on the CURRENT tab context, which is
          exactly what app.newAgent / app.newAgentDialog do, so the hints are
          true here. The branch context menu renders the same block WITHOUT
          them, because there the items act on that branch instead. */}
      <NewTabMenuItems
        shortcuts
        availableProviders={props.newTab.availableProviders}
        availableShells={props.newTab.availableShells}
        defaultShell={props.newTab.defaultShell}
        onNewAgent={handleNewAgent}
        onNewAgentAdvanced={() => props.newTab.onNewAgentAdvanced?.()}
        onNewTerminalWithShell={shell => props.newTab.onNewTerminalWithShell?.(shell)}
        onNewTerminalAdvanced={() => props.newTab.onNewTerminalAdvanced?.()}
      />
      <hr />
      <li class={menuSectionHeader}>Advanced</li>
      <DropdownMenuCheckableItem
        kind="checkbox"
        label="Expand agent thoughts"
        checked={prefs.expandAgentThoughts()}
        onSelect={() => prefs.setExpandAgentThoughts(!prefs.expandAgentThoughts())}
      />
      <DropdownMenuCheckableItem
        kind="checkbox"
        label="Show hidden messages"
        checked={prefs.showHiddenMessages()}
        onSelect={() => prefs.setShowHiddenMessages(!prefs.showHiddenMessages())}
      />
    </>
  )

  /**
   * The collapsed "+" new-tab dropdown, shared by the minimal strip variant
   * and the mobile bar — one UI in two visibility regimes, kept in lockstep
   * by one component. On mobile the titlebar (and its app menu) is gone, so
   * About / Preferences / Log out hitch a ride here after Advanced.
   */
  const CollapsedNewTabMenu: Component<{ wrapperClass: string }> = menuProps => (
    <div class={menuProps.wrapperClass}>
      <DropdownMenu
        trigger={triggerProps => (
          <IconButton
            icon={Plus}
            iconSize="md"
            size="md"
            data-testid="collapsed-new-tab-button"
            {...triggerProps}
          />
        )}
      >
        {renderMoreMenuItems()}
        <Show when={props.mobile}>
          <hr />
          <li class={menuSectionHeader}>App</li>
          <UserMenuItems />
        </Show>
      </DropdownMenu>
    </div>
  )

  return (
    <div class={styles.tabBar} data-testid="tab-bar">
      <Show
        when={props.mobile}
        fallback={(
          <>
            <div
              role="tablist"
              ref={(el) => {
                tabListRef = el
                zoneDroppable?.(el)
              }}
              class={styles.tabList}
              classList={{ [styles.tabListDropTarget]: isDropTarget() }}
              data-testid="tab-list"
              onDblClick={(e: MouseEvent) => {
                const target = e.target as HTMLElement
                if (target.closest('[data-testid="tab"]'))
                  return
                props.newTab.onNewAgentAdvanced?.()
              }}
            >
              <ErrorBoundary fallback={(
                // No sortable: this fallback exists to render the strip when the drag
                // machinery is what threw, so it must not touch it. The menu
                // anchor survives — setupTabRowDnd creates it unconditionally.
                <KeyedFor each={ids()} lookup={key => tabByKey().get(key)} rowSetup={setupTabRowDnd}>
                  {(tab, _key, dnd) => renderStripRow(tab, dnd)}
                </KeyedFor>
              )}
              >
                <SortableProvider ids={ids()}>
                  <KeyedFor
                    each={ids()}
                    lookup={key => tabByKey().get(key)}
                    rowSetup={setupTabRowDnd}
                  >
                    {(tab, _key, dnd) => renderStripRow(tab, dnd)}
                  </KeyedFor>
                </SortableProvider>
              </ErrorBoundary>
            </div>

            <Show when={props.newTab.showAddButton}>
              {/* Full / Compact: individual new-tab buttons */}
              <div class={styles.newTabWrapper}>
                <Show
                  when={mruProviders().length > 0}
                  fallback={(
                    <TabBarTooltip text="No agents available">
                      <IconButton
                        icon={Bot}
                        iconSize="md"
                        size="md"
                        state={IconButtonState.Disabled}
                        data-testid="new-agent-button-disabled"
                      />
                    </TabBarTooltip>
                  )}
                >
                  <For each={mruProviders()}>
                    {provider => (
                      <TabBarTooltip text={shortcutHint(`New ${agentProviderLabel(provider)} agent`, 'app.newAgent')}>
                        <Show
                          when={props.newTab.newAgentLoadingProvider !== provider}
                          fallback={(
                            <IconButton
                              icon={Bot}
                              iconSize="md"
                              size="md"
                              state={IconButtonState.Loading}
                              data-testid={`new-agent-button-${provider}`}
                            />
                          )}
                        >
                          <button
                            type="button"
                            class={providerButton}
                            data-testid={`new-agent-button-${provider}`}
                            onClick={() => handleNewAgent(provider)}
                          >
                            <AgentProviderIcon provider={provider} size={16} />
                          </button>
                        </Show>
                      </TabBarTooltip>
                    )}
                  </For>
                </Show>
                <TabBarTooltip text={shortcutHint(newTerminalLabel(), 'app.newTerminal')}>
                  <IconButton
                    icon={Terminal}
                    iconSize="md"
                    size="md"
                    state={props.newTab.newTerminalLoading ? IconButtonState.Loading : IconButtonState.Enabled}
                    data-testid="new-terminal-button"
                    onClick={() => props.newTab.onNewTerminal()}
                  />
                </TabBarTooltip>
                <DropdownMenu
                  trigger={triggerProps => (
                    <TabBarTooltip text="More options">
                      <IconButton
                        icon={Plus}
                        iconSize="md"
                        size="md"
                        state={props.newTab.newShellLoading ? IconButtonState.Loading : IconButtonState.Enabled}
                        data-testid="tab-more-menu"
                        {...triggerProps}
                      />
                    </TabBarTooltip>
                  )}
                >
                  {renderMoreMenuItems()}
                </DropdownMenu>
              </div>

              {/* Minimal: collapsed "+" button with new-tab + more options */}
              <CollapsedNewTabMenu wrapperClass={styles.collapsedNewTab} />

              {/* Micro: collapsed "..." button with everything including tile actions */}
              <div class={styles.collapsedOverflow}>
                <DropdownMenu
                  trigger={triggerProps => (
                    <IconButton
                      icon={Ellipsis}
                      iconSize="md"
                      size="md"
                      data-testid="collapsed-overflow-button"
                      {...triggerProps}
                    />
                  )}
                >
                  {renderMoreMenuItems()}
                  <Show when={props.tileActions}>
                    {actions => (
                      <>
                        <hr />
                        <li class={menuSectionHeader}>Tile</li>
                        <TileActionsMenu
                          actions={actions()}
                          withIcons
                          // Default size 2×2 from the overflow menu — the micro
                          // path doesn't have room for a hover-grid. Power users
                          // can use the full-mode popover.
                          onMakeGridClick={() => actions().onMakeGrid(2, 2)}
                          makeGridLabel="Make a 2×2 grid"
                        />
                      </>
                    )}
                  </Show>
                </DropdownMenu>
              </div>
            </Show>
          </>
        )}
      >
        {(mobile) => {
          // Closing the last tab from inside the sheet removes the chip —
          // the sheet's only toggle — so the sheet closes with it instead
          // of staying open over the empty tile state with no bar control
          // left to dismiss it by (the scrim still works, but it should
          // not have to).
          createEffect(() => {
            if (props.tabs.length === 0 && mobile().sheetOpen())
              mobile().onCloseSheet()
          })
          return (
            <>
              {/* Mobile: the strip collapses to a chip that opens the tab sheet.
                  The chip is hidden while the tile has no tabs — a "0" chip that
                  toggles an empty sheet explains nothing, and the main area's
                  empty-state buttons are the affordance for creating the first
                  tab. Its flex role (filling the middle so the files toggle
                  lands at the right end) is held by a spacer in its absence. */}
              <IconButton
                icon={Menu}
                iconSize="lg"
                size="xl"
                aria-label="Toggle workspaces"
                onClick={() => mobile().onToggleDrawer('left')}
              />
              <Show
                when={props.tabs.length > 0}
                fallback={<div class={styles.mobileBarSpacer} data-testid="tab-bar-spacer" aria-hidden="true" />}
              >
                <button
                  type="button"
                  class={styles.tabChip}
                  data-testid="tab-chip"
                  aria-haspopup="dialog"
                  aria-expanded={mobile().sheetOpen()}
                  onClick={() => mobile().onToggleSheet()}
                >
                  {/* Always truthy here — the chip renders only with tabs — but
                      `<Show>` is still the narrowest way to hand the row a live
                      tab accessor. */}
                  <Show when={chipTab()}>
                    {tab => (
                      <>
                        <span class={styles.tabIcon}>
                          <TabTypeIcon tab={tab()} />
                        </span>
                        <span class={styles.mobileClippedLabel}>{tabDisplayLabel(tab())}</span>
                        <Show when={tab().hasNotification}>
                          <span class={styles.tabNotification} data-testid="tab-notification" />
                        </Show>
                      </>
                    )}
                  </Show>
                  <span class={styles.tabChipCount} data-testid="tab-chip-count">{props.tabs.length}</span>
                  <ChevronDown size={14} class={styles.tabChipChevron} />
                </button>
              </Show>

              {/* Mobile keeps the single "+" new-tab dropdown. The mobile bar
                  has no [data-tile-size] ancestor (it renders outside any Tile),
                  so the tile-size rules that reveal `collapsedNewTab` never
                  apply — this variant is visible on its own. */}
              <Show when={props.newTab.showAddButton}>
                <CollapsedNewTabMenu wrapperClass={styles.mobileNewTab} />
              </Show>

              <IconButton
                icon={PanelRight}
                iconSize="lg"
                size="xl"
                aria-label="Toggle files"
                onClick={() => mobile().onToggleDrawer('right')}
              />
            </>
          )
        }}
      </Show>

      {/* The mobile tab sheet, fixed-positioned within the tab bar's stacking
          context — which itself paints above the drawers — so it covers an
          open drawer. Its open state and mutual exclusion with the drawers
          live in the shell's overlay state, not here. */}
      <Show when={props.mobile}>
        {mobile => (
          <TabSheet
            open={mobile().sheetOpen}
            onClose={mobile().onCloseSheet}
            tileId={props.tileId}
            tabCount={() => props.tabs.length}
          >
            <Show
              when={ids().length > 0}
              fallback={<div class={styles.sheetEmpty}>No tabs</div>}
            >
              <ErrorBoundary fallback={(
                <KeyedFor each={ids()} lookup={key => tabByKey().get(key)} rowSetup={setupTabRowDnd}>
                  {(tab, _key, dnd) => renderSheetRow(tab, dnd)}
                </KeyedFor>
              )}
              >
                <SortableProvider ids={ids()}>
                  <KeyedFor
                    each={ids()}
                    lookup={key => tabByKey().get(key)}
                    rowSetup={setupTabRowDnd}
                  >
                    {(tab, _key, dnd) => renderSheetRow(tab, dnd)}
                  </KeyedFor>
                </SortableProvider>
              </ErrorBoundary>
            </Show>
          </TabSheet>
        )}
      </Show>
    </div>
  )
}
