import type { Component, JSX } from 'solid-js'
import type { Tab } from '~/stores/tab.store'
import { createDroppable, createSortable, SortableProvider, transformStyle } from '@thisbeyond/solid-dnd'
import Bot from 'lucide-solid/icons/bot'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import Columns2 from 'lucide-solid/icons/columns-2'
import Ellipsis from 'lucide-solid/icons/ellipsis'
import Menu from 'lucide-solid/icons/menu'
import PanelRight from 'lucide-solid/icons/panel-right'
import Plus from 'lucide-solid/icons/plus'
import Rows2 from 'lucide-solid/icons/rows-2'
import Terminal from 'lucide-solid/icons/terminal'
import X from 'lucide-solid/icons/x'
import { createSignal, ErrorBoundary, For, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { tabKey, TabType } from '~/stores/tab.store'
import { menuSectionHeader, monoFont } from '~/styles/shared.css'
import { iconSize } from '~/styles/tokens'
import { TABBAR_ZONE_PREFIX, useCrossTileDrag } from './CrossTileDragContext'
import * as styles from './TabBar.css'

const TabBarTooltip: Component<{ text: string, children: JSX.Element }> = tipProps => (
  <span class={styles.tooltipTrigger} title={tipProps.text}>
    {tipProps.children}
  </span>
)

const TabTextWithTooltip: Component<{ label: string }> = (props) => {
  return (
    <span class={styles.tabText} title={props.label}>
      {props.label}
    </span>
  )
}

function tabTypeLabel(type: TabType): string {
  switch (type) {
    case TabType.AGENT: return 'agent'
    case TabType.TERMINAL: return 'terminal'
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
  onNewAgent: () => void
  onNewTerminal: () => void
  availableShells?: string[]
  defaultShell?: string
  onNewTerminalWithShell?: (shell: string) => void
  onResumeSession?: () => void
  onNewAgentAdvanced?: () => void
  onNewTerminalAdvanced?: () => void
  newAgentLoading?: boolean
  newTerminalLoading?: boolean
  newShellLoading?: boolean
  closingTabKeys?: Set<string>
  isEditingRef?: (fn: () => boolean) => void
  isMobile?: boolean
  onToggleLeftSidebar?: () => void
  onToggleRightSidebar?: () => void
  tileActions?: TileActions
}

export const TabBar: Component<TabBarProps> = (props) => {
  const [editingTabKey, setEditingTabKey] = createSignal<string | null>(null)
  const [editingValue, setEditingValue] = createSignal('')

  // Cross-tile drag context (may not be available on mobile single-tile layout)
  let crossTileDrag: ReturnType<typeof useCrossTileDrag> | undefined
  try {
    crossTileDrag = useCrossTileDrag()
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

  const tabLabel = (tab: Tab): string => {
    if (tab.title)
      return tab.title
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

  // Zone droppable for cross-tile drops (drop target for the whole tab bar area)
  // May fail if DragDropProvider context isn't available (e.g. during rapid tab creation)
  let zoneDroppable: ReturnType<typeof createDroppable> | undefined
  try {
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
          if (props.closingTabKeys?.has(tabKey(tab)))
            return
          props.onClose(tab)
        }
      }}
      onDblClick={(e: MouseEvent) => {
        e.preventDefault()
        e.stopPropagation()
        startEditing(tab)
      }}
    >
      <span class={styles.tabIcon}>
        {tab.type === TabType.AGENT ? <Bot size={14} /> : <Terminal size={14} />}
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
            if (e.key === 'Enter') {
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
    </div>
  )

  // Shared menu items for "More options" (used in full, collapsed-new-tab, and collapsed-overflow menus)
  const renderMoreMenuItems = () => (
    <>
      <li class={menuSectionHeader}>Agents</li>
      <button role="menuitem" onClick={() => props.onNewAgentAdvanced?.()}>
        New agent...
      </button>
      <button role="menuitem" onClick={() => props.onResumeSession?.()}>
        Resume an existing session
      </button>
      <hr />
      <li class={menuSectionHeader}>Terminals</li>
      <button role="menuitem" onClick={() => props.onNewTerminalAdvanced?.()}>
        New terminal...
      </button>
      <For each={props.availableShells ?? []}>
        {shell => (
          <button role="menuitem" onClick={() => props.onNewTerminalWithShell?.(shell)}>
            <span class={monoFont}>{shell}</span>
            <Show when={shell === props.defaultShell}>
              <span class={styles.shellDefault}>(default)</span>
            </Show>
          </button>
        )}
      </For>
    </>
  )

  return (
    <div class={styles.tabBar} data-testid="tab-bar">
      <Show when={props.isMobile}>
        <IconButton
          icon={Menu}
          iconSize={iconSize.lg}
          size="xl"
          aria-label="Toggle workspaces"
          onClick={() => props.onToggleLeftSidebar?.()}
        />
      </Show>
      <div
        role="tablist"
        ref={zoneDroppable}
        class={styles.tabList}
        classList={{ [styles.tabListDropTarget]: isDropTarget() }}
        data-testid="tab-list"
        onDblClick={(e: MouseEvent) => {
          const target = e.target as HTMLElement
          if (target.closest('[data-testid="tab"]'))
            return
          props.onNewAgent()
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
          <TabBarTooltip text="New agent">
            <IconButton
              icon={Bot}
              iconSize={iconSize.md}
              size="md"
              state={props.newAgentLoading ? IconButtonState.Loading : IconButtonState.Enabled}
              data-testid="new-agent-button"
              onClick={() => props.onNewAgent()}
            />
          </TabBarTooltip>
          <TabBarTooltip text="New terminal">
            <IconButton
              icon={Terminal}
              iconSize={iconSize.md}
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
                  icon={ChevronDown}
                  iconSize={iconSize.md}
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
                iconSize={iconSize.md}
                size="md"
                data-testid="collapsed-new-tab-button"
                {...triggerProps}
              />
            )}
          >
            <button role="menuitem" onClick={() => props.onNewAgent()}>
              <Bot size={14} />
              {' New agent'}
            </button>
            <button role="menuitem" onClick={() => props.onNewTerminal()}>
              <Terminal size={14} />
              {' New terminal'}
            </button>
            <hr />
            {renderMoreMenuItems()}
          </DropdownMenu>
        </div>

        {/* Micro: collapsed "..." button with everything including tile actions */}
        <div class={styles.collapsedOverflow}>
          <DropdownMenu
            trigger={triggerProps => (
              <IconButton
                icon={Ellipsis}
                iconSize={iconSize.md}
                size="md"
                data-testid="collapsed-overflow-button"
                {...triggerProps}
              />
            )}
          >
            <button role="menuitem" onClick={() => props.onNewAgent()}>
              <Bot size={14} />
              {' New agent'}
            </button>
            <button role="menuitem" onClick={() => props.onNewTerminal()}>
              <Terminal size={14} />
              {' New terminal'}
            </button>
            <hr />
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
                      <Columns2 size={14} />
                      {' Split vertical'}
                    </button>
                    <button
                      role="menuitem"
                      onClick={() => actions().onSplitVertical()}
                    >
                      <Rows2 size={14} />
                      {' Split horizontal'}
                    </button>
                  </Show>
                  <Show when={actions().canClose}>
                    <button
                      role="menuitem"
                      onClick={() => actions().onClose()}
                    >
                      <X size={14} />
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
          iconSize={iconSize.lg}
          size="xl"
          aria-label="Toggle files"
          onClick={() => props.onToggleRightSidebar?.()}
        />
      </Show>
    </div>
  )
}
