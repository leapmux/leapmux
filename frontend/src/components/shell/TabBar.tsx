import type { Component, JSX } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { Tab } from '~/stores/tab.store'
import { createDroppable, createSortable, SortableProvider, transformStyle } from '@thisbeyond/solid-dnd'
import Bot from 'lucide-solid/icons/bot'
import Columns2 from 'lucide-solid/icons/columns-2'
import Ellipsis from 'lucide-solid/icons/ellipsis'
import FileText from 'lucide-solid/icons/file-text'
import Menu from 'lucide-solid/icons/menu'
import PanelRight from 'lucide-solid/icons/panel-right'
import Plus from 'lucide-solid/icons/plus'
import Rows2 from 'lucide-solid/icons/rows-2'
import Terminal from 'lucide-solid/icons/terminal'
import X from 'lucide-solid/icons/x'
import { createSignal, ErrorBoundary, For, onCleanup, onMount, Show } from 'solid-js'
import { AgentProviderIcon, agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { DropdownMenu, DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { Tooltip } from '~/components/common/Tooltip'
import { usePreferences } from '~/context/PreferencesContext'
import { useMruProviders } from '~/hooks/useMruProviders'
import { getShortcutHint, shortcutHint } from '~/lib/shortcuts/display'
import { canCloseTab, tabKey, TabType } from '~/stores/tab.store'
import { menuSectionHeader } from '~/styles/shared.css'
import * as styles from './TabBar.css'
import { TABBAR_ZONE_PREFIX, useTabDrag } from './TabDragContext'

const MENU_CHECK = '\u2713' // ✓

function renderToggleMenuLabel(label: string, checked: boolean): JSX.Element {
  return (
    <span class={styles.toggleMenuLabel}>
      <span class={styles.toggleMenuIndicator} aria-hidden="true">{checked ? MENU_CHECK : ''}</span>
      <span>{label}</span>
    </span>
  )
}

const TabBarTooltip: Component<{ text: string, children: JSX.Element }> = tipProps => (
  <Tooltip text={tipProps.text}>
    <span class={styles.tooltipTrigger}>
      {tipProps.children}
    </span>
  </Tooltip>
)

const TabTextWithTooltip: Component<{ label: string }> = (props) => {
  return (
    <Tooltip text={props.label}>
      <span class={styles.tabText}>
        {props.label}
      </span>
    </Tooltip>
  )
}

function tabTypeLabel(type: TabType): string {
  switch (type) {
    case TabType.AGENT: return 'agent'
    case TabType.TERMINAL: return 'terminal'
    case TabType.FILE: return 'file'
    default: return 'unknown'
  }
}

export interface TileActions {
  canSplit: boolean
  canClose: boolean
  onSplitHorizontal: () => void
  onSplitVertical: () => void
  onClose: () => void
}

interface TabBarProps {
  tileId: string
  tabs: Tab[]
  activeTabKey: string | null
  showAddButton: boolean
  onSelect: (tab: Tab) => void
  onClose: (tab: Tab) => void
  onRename: (tab: Tab, title: string) => void
  onNewAgent: (provider?: AgentProvider) => void
  onNewTerminal: () => void
  availableProviders?: AgentProvider[]
  availableShells?: string[]
  defaultShell?: string
  onNewTerminalWithShell?: (shell: string) => void
  onNewAgentAdvanced?: () => void
  onNewTerminalAdvanced?: () => void
  newAgentLoadingProvider?: AgentProvider | null
  newTerminalLoading?: boolean
  newShellLoading?: boolean
  hasActiveTabContext?: boolean
  closingTabKeys?: Set<string>
  isEditingRef?: (fn: () => boolean) => void
  isMobile?: boolean
  onToggleLeftSidebar?: () => void
  onToggleRightSidebar?: () => void
  tileActions?: TileActions
  readOnly?: boolean
}

export const TabBar: Component<TabBarProps> = (props) => {
  const prefs = usePreferences()
  const { mruProviders, recordProviderUse } = useMruProviders(() => props.availableProviders ?? [], 2)
  const handleNewAgent = (provider?: AgentProvider) => {
    if (provider !== undefined)
      recordProviderUse(provider)
    props.onNewAgent(provider)
  }
  const newTerminalLabel = () => props.hasActiveTabContext ? 'New terminal at the current working directory' : 'New terminal...'

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

  const tabLabel = (tab: Tab): string => {
    if (tab.title)
      return tab.title
    if (tab.type === TabType.FILE)
      return tab.filePath?.split('/').pop() ?? 'File'
    return tab.type === TabType.AGENT ? 'Agent' : 'Terminal'
  }

  const startEditing = (tab: Tab) => {
    setEditingTabKey(tabKey(tab))
    setEditingValue(tabLabel(tab))
  }

  const commitEdit = (tab: Tab) => {
    if (editCancelled) {
      editCancelled = false
      return
    }
    const value = editingValue().trim()
    if (value && value !== tabLabel(tab)) {
      props.onRename(tab, value)
    }
    setEditingTabKey(null)
  }

  const cancelEdit = () => {
    editCancelled = true
    setEditingTabKey(null)
  }

  const ids = () => props.tabs.map(t => tabKey(t))

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

  const renderTab = (tab: Tab, sortable?: ReturnType<typeof createSortable>) => (
    <div
      role="tab"
      ref={sortable}
      tabIndex={0}
      aria-selected={props.activeTabKey === tabKey(tab)}
      class={styles.tab}
      classList={{ [styles.tabDragging]: sortable?.isActiveDraggable }}
      style={sortable?.transform ? transformStyle(sortable.transform) : undefined}
      data-testid="tab"
      data-tab-type={tabTypeLabel(tab.type)}
      data-tab-id={tab.id}
      onClick={() => handleTabChange(tabKey(tab))}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          handleTabChange(tabKey(tab))
        }
      }}
      onAuxClick={(e: MouseEvent) => {
        if (e.button === 1) {
          e.preventDefault()
          if ((props.readOnly && tab.type !== TabType.FILE) || props.closingTabKeys?.has(tabKey(tab)))
            return
          props.onClose(tab)
        }
      }}
      onContextMenu={(e: MouseEvent) => e.preventDefault()}
      onDblClick={(e: MouseEvent) => {
        e.preventDefault()
        e.stopPropagation()
        if (tab.type !== TabType.FILE && !props.readOnly)
          startEditing(tab)
      }}
    >
      <span class={styles.tabIcon}>
        {tab.type === TabType.AGENT ? <AgentProviderIcon provider={tab.agentProvider} size={14} /> : tab.type === TabType.FILE ? <Icon icon={FileText} size="sm" /> : <Icon icon={Terminal} size="sm" />}
      </span>
      <Show
        when={editingTabKey() === tabKey(tab)}
        fallback={<TabTextWithTooltip label={tabLabel(tab)} />}
      >
        <input
          class={styles.tabEditInput}
          type="text"
          value={editingValue()}
          onInput={e => setEditingValue(e.currentTarget.value)}
          onKeyDown={(e) => {
            e.stopPropagation()
            if (e.key === 'Enter') {
              e.preventDefault()
              commitEdit(tab)
            }
            else if (e.key === 'Escape') {
              cancelEdit()
            }
          }}
          onBlur={() => commitEdit(tab)}
          onClick={e => e.stopPropagation()}
          ref={(el) => {
            requestAnimationFrame(() => {
              el.focus()
              el.select()
            })
          }}
        />
      </Show>
      <Show when={tab.hasNotification}>
        <span class={styles.tabNotification} data-testid="tab-notification" />
      </Show>
      <Show when={canCloseTab(props.readOnly, tab)}>
        <IconButton
          icon={X}
          class={styles.tabClose}
          state={props.closingTabKeys?.has(tabKey(tab)) ? IconButtonState.Loading : IconButtonState.Enabled}
          data-testid="tab-close"
          onPointerDown={e => e.stopPropagation()}
          onClick={(e) => {
            e.stopPropagation()
            if (props.closingTabKeys?.has(tabKey(tab)))
              return
            props.onClose(tab)
          }}
        />
      </Show>
    </div>
  )

  // Shared menu items for "More options" (used in full, collapsed-new-tab, and collapsed-overflow menus)
  const renderMoreMenuItems = () => (
    <>
      <li class={menuSectionHeader}>Agents</li>
      <Show when={props.availableProviders?.length}>
        <li class={styles.providerIconsRow}>
          <For each={props.availableProviders}>
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
      <button role="menuitem" onClick={() => props.onNewAgentAdvanced?.()}>
        <DropdownMenuItemContent
          label="New agent..."
          shortcut={getShortcutHint('app.newAgentDialog')}
        />
      </button>
      <hr />
      <li class={menuSectionHeader}>Terminals</li>
      <button role="menuitem" onClick={() => props.onNewTerminalAdvanced?.()}>
        <DropdownMenuItemContent
          label="New terminal..."
          shortcut={getShortcutHint('app.newTerminalDialog')}
        />
      </button>
      <For each={props.availableShells ?? []}>
        {shell => (
          <button role="menuitem" onClick={() => props.onNewTerminalWithShell?.(shell)}>
            <code>{shell}</code>
            <Show when={shell === props.defaultShell}>
              <span class={styles.shellDefault}>(default)</span>
            </Show>
          </button>
        )}
      </For>
      <hr />
      <li class={menuSectionHeader}>Advanced</li>
      <button
        role="menuitem"
        onClick={(e) => {
          e.preventDefault()
          prefs.setExpandAgentThoughts(!prefs.expandAgentThoughts())
        }}
      >
        <DropdownMenuItemContent label={renderToggleMenuLabel('Expand agent thoughts', prefs.expandAgentThoughts())} />
      </button>
      <button
        role="menuitem"
        onClick={(e) => {
          e.preventDefault()
          prefs.setShowHiddenMessages(!prefs.showHiddenMessages())
        }}
      >
        <DropdownMenuItemContent label={renderToggleMenuLabel('Show hidden messages', prefs.showHiddenMessages())} />
      </button>
    </>
  )

  return (
    <div class={styles.tabBar} data-testid="tab-bar">
      <Show when={props.isMobile}>
        <IconButton
          icon={Menu}
          iconSize="lg"
          size="xl"
          aria-label="Toggle workspaces"
          onClick={() => props.onToggleLeftSidebar?.()}
        />
      </Show>
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
          props.onNewAgentAdvanced?.()
        }}
      >
        <ErrorBoundary fallback={(
          <For each={props.tabs}>
            {tab => renderTab(tab)}
          </For>
        )}
        >
          <SortableProvider ids={ids()}>
            <For each={props.tabs}>
              {(tab) => {
                let sortable: ReturnType<typeof createSortable> | undefined
                try {
                  sortable = createSortable(tabKey(tab))
                }
                catch { /* DnD context not ready */ }
                return renderTab(tab, sortable)
              }}
            </For>
          </SortableProvider>
        </ErrorBoundary>
      </div>
      <Show when={props.showAddButton}>
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
                    when={props.newAgentLoadingProvider !== provider}
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
              state={props.newTerminalLoading ? IconButtonState.Loading : IconButtonState.Enabled}
              data-testid="new-terminal-button"
              onClick={() => props.onNewTerminal()}
            />
          </TabBarTooltip>
          <DropdownMenu
            trigger={triggerProps => (
              <TabBarTooltip text="More options">
                <IconButton
                  icon={Plus}
                  iconSize="md"
                  size="md"
                  state={props.newShellLoading ? IconButtonState.Loading : IconButtonState.Enabled}
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
                  <Show when={actions().canSplit}>
                    <button
                      role="menuitem"
                      onClick={() => actions().onSplitHorizontal()}
                    >
                      <Icon icon={Columns2} size="sm" />
                      <DropdownMenuItemContent
                        label="Split vertical"
                        shortcut={getShortcutHint('app.splitTileHorizontal')}
                      />
                    </button>
                    <button
                      role="menuitem"
                      onClick={() => actions().onSplitVertical()}
                    >
                      <Icon icon={Rows2} size="sm" />
                      <DropdownMenuItemContent
                        label="Split horizontal"
                        shortcut={getShortcutHint('app.splitTileVertical')}
                      />
                    </button>
                  </Show>
                  <Show when={actions().canClose}>
                    <button
                      role="menuitem"
                      onClick={() => actions().onClose()}
                    >
                      <Icon icon={X} size="sm" />
                      {' Close tile'}
                    </button>
                  </Show>
                </>
              )}
            </Show>
          </DropdownMenu>
        </div>
      </Show>
      <Show when={props.isMobile}>
        <IconButton
          icon={PanelRight}
          iconSize="lg"
          size="xl"
          aria-label="Toggle files"
          onClick={() => props.onToggleRightSidebar?.()}
        />
      </Show>
    </div>
  )
}
