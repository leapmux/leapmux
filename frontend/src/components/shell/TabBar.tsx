import type { Component, JSX } from 'solid-js'
import type { TileActions } from './TileActionsMenu'
import type { TabPopAction } from '~/components/common/TabContextMenu'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import type { Tab } from '~/stores/tab.types'
import { createDroppable, createSortable, SortableProvider, transformStyle } from '@thisbeyond/solid-dnd'
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
import { createContextMenuAnchor, DropdownMenu, DropdownMenuCheckableItem, DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { TabContextMenu } from '~/components/common/TabContextMenu'
import { TabTypeIcon } from '~/components/common/TabTypeIcon'
import { Tooltip } from '~/components/common/Tooltip'
import { usePreferences } from '~/context/PreferencesContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { useMruProviders } from '~/hooks/useMruProviders'
import { attachDragActivators } from '~/lib/dragActivators'
import { createKeyedRows, KeyedFor } from '~/lib/keyedRows'
import { getShortcutHintsText, shortcutHint } from '~/lib/shortcuts/display'
import { canCloseTab, tabDisplayLabel, tabKey, tabTooltipShowWhen, tabTooltipText, terminalProgressBarProps, terminalProgressVisible } from '~/stores/tab.helpers'
import { isTerminalTab } from '~/stores/tab.types'
import { menuSectionHeader } from '~/styles/shared.css'
import * as styles from './TabBar.css'
import { TABBAR_ZONE_PREFIX, useTabDrag } from './TabDragContext'
import { terminalStatusClassList } from './terminalStatus'
import { TileActionsMenu } from './TileActionsMenu'

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

function tabTypeLabel(type: TabType): string {
  switch (type) {
    case TabType.AGENT: return 'agent'
    case TabType.TERMINAL: return 'terminal'
    case TabType.FILE: return 'file'
    default: return 'unknown'
  }
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
 * Mobile sidebar toggles rendered in the tab bar header. The presence
 * of the prop bundle itself signals "we're in mobile-layout mode" —
 * pass `undefined` (or omit) on desktop.
 */
interface TabBarMobileProps {
  onToggleLeftSidebar?: () => void
  onToggleRightSidebar?: () => void
  /** Close both mobile drawers, used for mutual exclusion with the tab sheet. */
  onCloseSidebars?: () => void
}

interface TabBarProps {
  tileId: string
  tabs: Tab[]
  activeTabKey: string | null
  readOnly?: boolean
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
  let sheetPanelRef: HTMLDivElement | undefined

  /** The tab the mobile chip speaks for: the active one, falling back to the first. */
  const chipTab = () => props.tabs.find(t => tabKey(t) === props.activeTabKey) ?? props.tabs[0]

  const [sheetOpen, setSheetOpen] = createSignal(false)
  const openSheet = () => {
    props.mobile?.onCloseSidebars?.()
    setSheetOpen(true)
  }
  // Focus the panel when it opens so its Escape handler has a target. The
  // panel is always mounted (class-flipped for the slide), so the focus call
  // is what makes the keyboard path work at all.
  createEffect(() => {
    if (sheetOpen())
      requestAnimationFrame(() => sheetPanelRef?.focus({ preventScroll: true }))
  })

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
   */
  interface TabRowDnd {
    sortable?: ReturnType<typeof createSortable>
    /** Row-element anchor: the context menu and the guarded body activators. */
    rowEl: () => HTMLElement | undefined
    setRowEl: (el: HTMLElement) => void
  }

  const setupTabRowDnd = (key: string): TabRowDnd | undefined => {
    try {
      // The key is the row's identity for the whole of its life now,
      // so the sortable id is fixed at creation rather than re-derived
      // from a `Tab` the row no longer owns. Created HERE, in the
      // for-row owner, so it outlives a tick where the lookup misses.
      const sortable = createSortable(key)
      const [rowEl, setRowEl] = createContextMenuAnchor()
      // Mouse-only activation on the row body; the grip carries the raw
      // handlers, so touch drags start there and nowhere else.
      attachDragActivators(rowEl, () => sortable.dragActivators, { touch: 'block' })
      return { sortable, rowEl, setRowEl }
    }
    catch { /* DnD context not ready */ }
    return undefined
  }

  // `tab` is an ACCESSOR, not a value: the row outlives any single `Tab` object
  // now that the `<For>` keys on `ids()`, so every field has to be read through
  // it to stay live. Reading it once here would freeze the row at whatever the
  // tab looked like when it mounted.
  const renderTab = (tab: () => Tab, dnd?: TabRowDnd) => {
    const sortable = dnd?.sortable
    const isClosing = () => props.closingTabKeys?.has(tabKey(tab())) ?? false
    const canRename = () => tab().type !== TabType.FILE && !props.readOnly
    return (
      <div
        role="tab"
        ref={(el) => {
          dnd?.setRowEl(el)
          // Node registration only — activation is on the guarded body and
          // the grip above, never attached wholesale: a touch press on the
          // row must not reach the drag sensor at all.
          sortable?.ref?.(el)
        }}
        tabIndex={0}
        aria-selected={props.activeTabKey === tabKey(tab())}
        class={styles.tab}
        classList={{ [styles.tabDragging]: sortable?.isActiveDraggable }}
        style={sortable?.transform ? transformStyle(sortable.transform) : undefined}
        data-testid="tab"
        data-tab-type={tabTypeLabel(tab().type)}
        data-tab-id={tab().id}
        data-terminal-status={terminalStatusOf(tab())}
        onClick={() => handleTabChange(tabKey(tab()))}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            handleTabChange(tabKey(tab()))
          }
        }}
        onAuxClick={(e: MouseEvent) => {
          if (e.button === 1) {
            e.preventDefault()
            if ((props.readOnly && tab().type !== TabType.FILE) || props.closingTabKeys?.has(tabKey(tab())))
              return
            props.onClose(tab())
          }
        }}
        onDblClick={(e: MouseEvent) => {
          e.preventDefault()
          e.stopPropagation()
          if (tab().type !== TabType.FILE && !props.readOnly)
            startEditing(tab())
        }}
      >
        <DragHandle activators={() => sortable?.dragActivators} testId="tab-drag-handle" />
        <span class={styles.tabIcon}>
          <TabTypeIcon tab={tab()} />
        </span>
        <Show
          when={editingTabKey() === tabKey(tab())}
          fallback={<TabTextWithTooltip label={tabDisplayLabel(tab())} tooltip={tabTooltipText(tab())} showWhen={tabTooltipShowWhen(tab())} status={terminalStatusOf(tab())} />}
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
        <Show when={canCloseTab(props.readOnly, tab())}>
          <IconButton
            icon={X}
            class={styles.tabClose}
            state={props.closingTabKeys?.has(tabKey(tab())) ? IconButtonState.Loading : IconButtonState.Enabled}
            data-testid="tab-close"
            onPointerDown={e => e.stopPropagation()}
            onClick={(e) => {
              e.stopPropagation()
              if (props.closingTabKeys?.has(tabKey(tab())))
                return
              props.onClose(tab())
            }}
          />
        </Show>
        {/* Outside the close block: a tab that cannot be closed can still be
            renamed or popped out. The menu host collapses to `display: contents`,
            so it costs the tab strip no layout either way. */}
        <TabContextMenu
          contextMenuFor={dnd?.rowEl}
          data-testid="tab-bar-tab-menu"
          onRename={canRename() ? () => startEditing(tab()) : undefined}
          onClose={canCloseTab(props.readOnly, tab()) ? () => props.onClose(tab()) : undefined}
          isClosing={isClosing()}
          pop={props.tabPop?.(tab())}
        />
      </div>
    )
  }

  /**
   * A row of the mobile tab sheet. Same keyed-row contract as the strip — the
   * accessor keeps the row live, the DnD wiring comes from `setupTabRowDnd`
   * in the for-row owner — with activation split the same way: the grip for
   * touch, the guarded body for mouse, so a touch swipe scrolls the list, a
   * touch hold opens the context menu, and only the grip drags a finger.
   */
  const renderSheetRow = (tab: () => Tab, dnd?: TabRowDnd) => {
    const sortable = dnd?.sortable
    const isClosing = () => props.closingTabKeys?.has(tabKey(tab())) ?? false
    const canRename = () => tab().type !== TabType.FILE && !props.readOnly
    return (
      <div
        role="tab"
        ref={(el) => {
          dnd?.setRowEl(el)
          sortable?.ref?.(el)
        }}
        tabIndex={0}
        aria-selected={props.activeTabKey === tabKey(tab())}
        class={styles.sheetRow}
        classList={{ [styles.tabDragging]: sortable?.isActiveDraggable }}
        style={sortable?.transform ? transformStyle(sortable.transform) : undefined}
        data-testid="tab-sheet-row"
        data-tab-type={tabTypeLabel(tab().type)}
        data-tab-id={tab().id}
        onClick={() => {
          handleTabChange(tabKey(tab()))
          setSheetOpen(false)
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            handleTabChange(tabKey(tab()))
            setSheetOpen(false)
          }
        }}
      >
        <DragHandle visibility="always" activators={() => sortable?.dragActivators} testId="tab-sheet-drag-handle" />
        <span class={styles.tabIcon}>
          <TabTypeIcon tab={tab()} />
        </span>
        <Show
          when={editingTabKey() === tabKey(tab())}
          fallback={<span class={styles.sheetRowLabel}>{tabDisplayLabel(tab())}</span>}
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
        <Show when={canCloseTab(props.readOnly, tab())}>
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
        <TabContextMenu
          contextMenuFor={dnd?.rowEl}
          data-testid="tab-sheet-row-menu"
          onRename={canRename() ? () => startEditing(tab()) : undefined}
          onClose={canCloseTab(props.readOnly, tab()) ? () => props.onClose(tab()) : undefined}
          isClosing={isClosing()}
          pop={props.tabPop?.(tab())}
        />
      </div>
    )
  }

  // Shared menu items for "More options" (used in full, collapsed-new-tab, and collapsed-overflow menus)
  const renderMoreMenuItems = () => (
    <>
      <li class={menuSectionHeader}>Agents</li>
      <Show when={props.newTab.availableProviders?.length}>
        <li class={styles.providerIconsRow}>
          <For each={props.newTab.availableProviders}>
            {provider => (
              <TabBarTooltip text={shortcutHint(`New ${agentProviderLabel(provider)} agent`, 'app.newAgent')}>
                <button
                  type="button"
                  class={styles.providerButton}
                  onClick={() => handleNewAgent(provider)}
                >
                  <AgentProviderIcon provider={provider} size={16} />
                </button>
              </TabBarTooltip>
            )}
          </For>
        </li>
      </Show>
      <button role="menuitem" onClick={() => props.newTab.onNewAgentAdvanced?.()}>
        <DropdownMenuItemContent
          label="New agent..."
          shortcut={getShortcutHintsText('app.newAgentDialog')}
        />
      </button>
      <hr />
      <li class={menuSectionHeader}>Terminals</li>
      <button role="menuitem" onClick={() => props.newTab.onNewTerminalAdvanced?.()}>
        <DropdownMenuItemContent
          label="New terminal..."
          shortcut={getShortcutHintsText('app.newTerminalDialog')}
        />
      </button>
      <For each={props.newTab.availableShells ?? []}>
        {shell => (
          <button role="menuitem" onClick={() => props.newTab.onNewTerminalWithShell?.(shell)}>
            <code>{shell}</code>
            <Show when={shell === props.newTab.defaultShell}>
              <span class={styles.shellDefault}>(default)</span>
            </Show>
          </button>
        )}
      </For>
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

  return (
    <div class={styles.tabBar} data-testid="tab-bar">
      <Show when={props.mobile}>
        <IconButton
          icon={Menu}
          iconSize="lg"
          size="xl"
          aria-label="Toggle workspaces"
          onClick={() => {
            setSheetOpen(false)
            props.mobile?.onToggleLeftSidebar?.()
          }}
        />
      </Show>

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
                // machinery is what threw, so it must not touch it.
                <KeyedFor each={ids()} lookup={key => tabByKey().get(key)}>
                  {tab => renderTab(tab)}
                </KeyedFor>
              )}
              >
                <SortableProvider ids={ids()}>
                  <KeyedFor
                    each={ids()}
                    lookup={key => tabByKey().get(key)}
                    rowSetup={setupTabRowDnd}
                  >
                    {(tab, _key, dnd) => renderTab(tab, dnd)}
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
                            class={styles.providerButton}
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
              <div class={styles.collapsedNewTab}>
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
                </DropdownMenu>
              </div>

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
        {/* Mobile: the strip collapses to a chip that opens the tab sheet. */}
        <button
          type="button"
          class={styles.tabChip}
          data-testid="tab-chip"
          aria-haspopup="dialog"
          aria-expanded={sheetOpen()}
          onClick={() => {
            const tab = chipTab()
            if (!tab) {
              // Mirrors the strip's empty-area double-click: no tabs to list,
              // so the chip becomes a new-tab trigger.
              props.newTab.onNewAgentAdvanced?.()
              return
            }
            if (sheetOpen())
              setSheetOpen(false)
            else
              openSheet()
          }}
        >
          <Show
            when={chipTab()}
            fallback={<span class={styles.tabIcon} />}
          >
            {tab => (
              <>
                <span class={styles.tabIcon}>
                  <TabTypeIcon tab={tab()} />
                </span>
                <span class={styles.tabChipLabel}>{tabDisplayLabel(tab())}</span>
                <Show when={tab().hasNotification}>
                  <span class={styles.tabNotification} data-testid="tab-notification" />
                </Show>
              </>
            )}
          </Show>
          <span class={styles.tabChipCount} data-testid="tab-chip-count">{props.tabs.length}</span>
          <ChevronDown size={14} class={styles.tabChipChevron} />
        </button>

        {/* Mobile keeps the single "+" new-tab dropdown. The mobile bar has no
            [data-tile-size] ancestor (it renders outside any Tile), so the
            tile-size rules that reveal `collapsedNewTab` never apply — this
            variant is visible on its own. */}
        <Show when={props.newTab.showAddButton}>
          <div class={styles.mobileNewTab}>
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
            </DropdownMenu>
          </div>
        </Show>
      </Show>

      <Show when={props.mobile}>
        <IconButton
          icon={PanelRight}
          iconSize="lg"
          size="xl"
          aria-label="Toggle files"
          onClick={() => {
            setSheetOpen(false)
            props.mobile?.onToggleRightSidebar?.()
          }}
        />
      </Show>

      {/* The mobile tab list. Always mounted (class-flipped, like the mobile
          drawers, so the drop-down transition runs) and fixed-positioned
          within the tab bar's stacking context — which itself paints above
          the drawers — so the list covers an open drawer. It drops from
          directly under the bar, inside a clip window that keeps the slide
          from ever crossing the bar itself; the scrim starts below the bar
          too, so the bar stays bright and its chip toggles the sheet. */}
      <Show when={props.mobile}>
        <div
          class={styles.sheetOverlay}
          classList={{ [styles.sheetOverlayOpen]: sheetOpen() }}
          onClick={() => setSheetOpen(false)}
          aria-hidden="true"
          data-testid="tab-sheet-overlay"
        />
        <div class={styles.sheetPanelClip}>
          <div
            ref={(el) => {
              sheetPanelRef = el
            }}
            role="dialog"
            aria-modal="true"
            aria-label="Switch tab"
            class={styles.sheetPanel}
            classList={{ [styles.sheetPanelOpen]: sheetOpen() }}
            tabIndex={-1}
            data-testid="tab-sheet"
            onKeyDown={(e) => {
              if (e.key === 'Escape')
                setSheetOpen(false)
            }}
          >
            <div class={styles.sheetHeader}>
              <span class={styles.sheetTitle} data-testid="tab-sheet-title">
                {props.tabs.length}
                {' '}
                Tab
                {props.tabs.length === 1 ? '' : 's'}
              </span>
            </div>
            <div
              class={styles.sheetList}
              ref={(el) => {
                zoneDroppable?.(el)
              }}
              data-testid="tab-sheet-list"
            >
              <Show
                when={ids().length > 0}
                fallback={<div class={styles.sheetEmpty}>No tabs</div>}
              >
                <ErrorBoundary fallback={(
                  <KeyedFor each={ids()} lookup={key => tabByKey().get(key)}>
                    {tab => renderSheetRow(tab)}
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
            </div>
          </div>
        </div>
      </Show>
    </div>
  )
}
