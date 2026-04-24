import type { Component } from 'solid-js'
import type { Tab, TabItemOps } from '~/stores/tab.store'
import { createDraggable } from '@thisbeyond/solid-dnd'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FolderGit from 'lucide-solid/icons/folder-git'
import GitBranch from 'lucide-solid/icons/git-branch'
import Terminal from 'lucide-solid/icons/terminal'
import X from 'lucide-solid/icons/x'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { AgentProviderIcon } from '~/components/common/AgentProviderIcon'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { Tooltip } from '~/components/common/Tooltip'
import { SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { basename } from '~/lib/paths'
import { diffStatsFromTabFields } from '~/stores/gitFileStatus.store'
import { canCloseTab, tabKey } from '~/stores/tab.store'
import { terminalStatusClassList } from '../shell/terminalStatus'
import { DiffStatsBadge, LabelWithDiffStats } from '../tree/gitStatusUtils'
import * as shared from '../tree/sharedTree.css'
import { menuTrigger, sidebarActions } from '../tree/sidebarActions.css'
import * as css from './workspaceTabTree.css'

// --- Tab leaf node ---

const TabLeaf: Component<{
  tab: Tab
  workspaceId: string
  depth: number
  isActive: boolean
  isEditing: boolean
  editingValue: string
  onClick: () => void
  onDblClick: () => void
  onClose?: () => void
  isClosing?: boolean
  canClose: boolean
  onEditInput: (value: string) => void
  onEditCommit: () => void
  onEditCancel: () => void
}> = (props) => {
  /* eslint-disable solid/reactivity -- stable identifier for createDraggable */
  const draggable = createDraggable(
    `${SIDEBAR_TAB_PREFIX}${props.workspaceId}:${props.tab.type}:${props.tab.id}`,
    { title: props.tab.title || props.tab.id, type: props.tab.type },
  )
  /* eslint-enable solid/reactivity */

  return (
    <div
      ref={draggable}
      class={`${shared.node} ${css.leafNode} ${props.isActive ? css.leafActive : ''} ${draggable.isActiveDraggable ? css.leafDragging : ''}`}
      style={{ 'padding-left': `${4 + props.depth * 16}px` }}
      onClick={() => {
        if (!draggable.isActiveDraggable)
          props.onClick()
      }}
      onDblClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        props.onDblClick()
      }}
      onAuxClick={(e) => {
        if (e.button !== 1 || !props.canClose || props.isClosing)
          return
        e.preventDefault()
        e.stopPropagation()
        props.onClose?.()
      }}
      data-testid="tab-tree-leaf"
      data-tab-id={props.tab.id}
      data-terminal-status={props.tab.status}
    >
      <div class={shared.chevronPlaceholder} />
      <Show when={props.tab.type === TabType.AGENT} fallback={<Terminal size={14} class={css.tabIcon} />}>
        <AgentProviderIcon provider={props.tab.agentProvider} size={14} class={css.tabIcon} />
      </Show>
      <Show
        when={!props.isEditing}
        fallback={(
          <input
            class={css.tabRenameInput}
            type="text"
            value={props.editingValue}
            onInput={e => props.onEditInput(e.currentTarget.value)}
            onKeyDown={(e) => {
              e.stopPropagation()
              if (e.key === 'Enter') {
                e.preventDefault()
                props.onEditCommit()
              }
              else if (e.key === 'Escape') {
                props.onEditCancel()
              }
            }}
            onBlur={() => props.onEditCommit()}
            onClick={e => e.stopPropagation()}
            ref={(el) => {
              requestAnimationFrame(() => {
                el.focus()
                el.select()
              })
            }}
          />
        )}
      >
        <Tooltip text={props.tab.title || props.tab.id}>
          <span
            class={css.tabLabel}
            classList={terminalStatusClassList(props.tab.status)}
          >
            {props.tab.title || props.tab.id}
          </span>
        </Tooltip>
      </Show>
      <Show when={props.canClose}>
        <div class={`${sidebarActions} ${css.leafActions}`}>
          <IconButton
            icon={X}
            iconSize="sm"
            size="sm"
            class={menuTrigger}
            state={props.isClosing ? IconButtonState.Loading : IconButtonState.Enabled}
            data-testid="workspace-tab-close"
            onPointerDown={e => e.stopPropagation()}
            onClick={(e) => {
              e.stopPropagation()
              if (props.isClosing)
                return
              props.onClose?.()
            }}
          />
        </div>
      </Show>
    </div>
  )
}

// --- Public API ---

export interface WorkspaceTabTreeProps {
  tabs: Tab[]
  activeTabKey: string | null
  onTabClick: (type: TabType, id: string) => void
  tabItemOps?: TabItemOps
  readOnly?: boolean
  workspaceId: string
}

export const WorkspaceTabTree: Component<WorkspaceTabTreeProps> = (props) => {
  const tree = createMemo(() => buildTree(props.tabs))
  const storageKey = () => `leapmux:tabTree:${props.workspaceId}`

  // --- Tab rename editing state ---
  const [editingTabKey, setEditingTabKey] = createSignal<string | null>(null)
  const [editingValue, setEditingValue] = createSignal('')
  let editCancelled = false
  const canClose = (tab: Tab) => canCloseTab(props.readOnly, tab)

  const tabLabel = (tab: Tab): string => tab.title || tab.id

  const startEditing = (tab: Tab) => {
    if (props.readOnly || tab.type === TabType.FILE || !props.tabItemOps?.onRename)
      return
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
      props.tabItemOps?.onRename?.(tab, value)
    }
    setEditingTabKey(null)
  }

  const cancelEdit = () => {
    editCancelled = true
    setEditingTabKey(null)
  }

  function loadCollapsedState(): Record<string, boolean> {
    try {
      const stored = sessionStorage.getItem(storageKey())
      return stored ? JSON.parse(stored) : {}
    }
    catch {
      return {}
    }
  }

  // Collapse state keyed by group label
  const [collapsed, setCollapsed] = createSignal<Record<string, boolean>>(loadCollapsedState())

  function isCollapsed(key: string): boolean {
    return collapsed()[key] ?? false
  }

  function toggleCollapsed(key: string) {
    setCollapsed((prev) => {
      const next = { ...prev, [key]: !prev[key] }
      try {
        sessionStorage.setItem(storageKey(), JSON.stringify(next))
      }
      catch { /* quota */ }
      return next
    })
  }

  return (
    <div class={css.treeWrapper} data-testid="workspace-tab-tree">
      <Show when={tree().groups.length > 0}>
        <For each={tree().groups}>
          {group => (
            <>
              {/* Repo group header */}
              <div
                class={shared.node}
                style={{ 'padding-left': '20px' }}
                onClick={() => toggleCollapsed(group.repoKey)}
                data-testid="tab-tree-repo-group"
              >
                <ChevronRight
                  size={14}
                  class={`${shared.chevron} ${!isCollapsed(group.repoKey) ? shared.chevronExpanded : ''}`}
                />
                <FolderGit size={14} class={css.groupIcon} />
                <Tooltip content={<LabelWithDiffStats label={repoTooltip(group.repoKey)} stats={diffStatsFromTabFields(group)} />}>
                  <span class={css.groupLabelWithStats}>
                    {group.repoLabel}
                    <DiffStatsBadge stats={diffStatsFromTabFields(group)} />
                  </span>
                </Tooltip>
              </div>

              <div class={`${shared.childrenWrapper} ${!isCollapsed(group.repoKey) ? shared.childrenWrapperExpanded : ''}`}>
                <div class={shared.childrenInner}>
                  <For each={group.branches}>
                    {branch => (
                      <>
                        {/* Branch group header */}
                        <div
                          class={shared.node}
                          style={{ 'padding-left': '36px' }}
                          onClick={() => toggleCollapsed(`${group.repoKey}:${branch.branchName}`)}
                          data-testid="tab-tree-branch-group"
                        >
                          <ChevronRight
                            size={14}
                            class={`${shared.chevron} ${!isCollapsed(`${group.repoKey}:${branch.branchName}`) ? shared.chevronExpanded : ''}`}
                          />
                          <GitBranch size={14} class={css.groupIcon} />
                          <Tooltip content={<LabelWithDiffStats label={branch.branchName} stats={diffStatsFromTabFields(branch)} />}>
                            <span class={css.groupLabelWithStats}>
                              {branch.branchName}
                              <DiffStatsBadge stats={diffStatsFromTabFields(branch)} />
                            </span>
                          </Tooltip>
                        </div>

                        <div class={`${shared.childrenWrapper} ${!isCollapsed(`${group.repoKey}:${branch.branchName}`) ? shared.childrenWrapperExpanded : ''}`}>
                          <div class={shared.childrenInner}>
                            <For each={branch.tabs}>
                              {tab => (
                                <TabLeaf
                                  tab={tab}
                                  workspaceId={props.workspaceId}
                                  depth={3}
                                  isActive={tabKey(tab) === props.activeTabKey}
                                  isEditing={editingTabKey() === tabKey(tab)}
                                  editingValue={editingValue()}
                                  onClick={() => props.onTabClick(tab.type, tab.id)}
                                  onDblClick={() => startEditing(tab)}
                                  onClose={() => props.tabItemOps?.onClose?.(tab)}
                                  isClosing={props.tabItemOps?.closingKeys?.has(tabKey(tab))}
                                  canClose={canClose(tab)}
                                  onEditInput={v => setEditingValue(v)}
                                  onEditCommit={() => commitEdit(tab)}
                                  onEditCancel={cancelEdit}
                                />
                              )}
                            </For>
                          </div>
                        </div>
                      </>
                    )}
                  </For>
                </div>
              </div>
            </>
          )}
        </For>
      </Show>

      {/* Ungrouped tabs (no git info) */}
      <For each={tree().ungrouped}>
        {tab => (
          <TabLeaf
            tab={tab}
            workspaceId={props.workspaceId}
            depth={1}
            isActive={tabKey(tab) === props.activeTabKey}
            isEditing={editingTabKey() === tabKey(tab)}
            editingValue={editingValue()}
            onClick={() => props.onTabClick(tab.type, tab.id)}
            onDblClick={() => startEditing(tab)}
            onClose={() => props.tabItemOps?.onClose?.(tab)}
            isClosing={props.tabItemOps?.closingKeys?.has(tabKey(tab))}
            canClose={canClose(tab)}
            onEditInput={v => setEditingValue(v)}
            onEditCommit={() => commitEdit(tab)}
            onEditCancel={cancelEdit}
          />
        )}
      </For>
    </div>
  )
}

// --- Grouping logic ---

interface BranchGroup {
  branchName: string
  tabs: Tab[]
  diffAdded: number
  diffDeleted: number
  diffUntracked: number
}

interface RepoGroup {
  repoKey: string
  repoLabel: string
  branches: BranchGroup[]
  diffAdded: number
  diffDeleted: number
  diffUntracked: number
}

interface TabTree {
  groups: RepoGroup[]
  ungrouped: Tab[]
}

const SSH_ORIGIN_RE = /^git@([^:]+):(.+)$/
const PROTOCOL_PREFIX_RE = /^https?:\/\//
const TRAILING_DOT_GIT_RE = /\.git$/
const TRAILING_SLASH_RE = /\/$/

/**
 * Prefix for the synthetic repo key used for origin-less local repos. The
 * full key is `LOCAL_REPO_KEY_PREFIX + toplevel`, so two distinct local
 * repos sharing a workspace sort under separate groups. Chosen so it
 * can't collide with any real origin URL — git origin URLs cannot begin
 * with a null byte.
 */
export const LOCAL_REPO_KEY_PREFIX = '\x00local:'

/**
 * Tooltip text for a repo group header. Returns the full origin URL for
 * remote repos, or the full toplevel path for origin-less local repos.
 */
function repoTooltip(repoKey: string): string {
  if (repoKey.startsWith(LOCAL_REPO_KEY_PREFIX))
    return repoKey.substring(LOCAL_REPO_KEY_PREFIX.length)
  return repoKey
}

export function formatGitOriginUrl(url: string): string {
  if (!url)
    return ''
  let result = url
  // Convert SSH format: git@github.com:org/repo -> github.com/org/repo
  const sshMatch = result.match(SSH_ORIGIN_RE)
  if (sshMatch)
    result = `${sshMatch[1]}/${sshMatch[2]}`
  // Strip protocols
  result = result.replace(PROTOCOL_PREFIX_RE, '')
  // Strip trailing .git
  result = result.replace(TRAILING_DOT_GIT_RE, '')
  // Strip trailing slash
  result = result.replace(TRAILING_SLASH_RE, '')
  return result
}

/**
 * Computes the grouping key and display label for a tab's repository. Order
 * of precedence:
 *   1. gitOriginUrl — a remote we can format nicely.
 *   2. gitToplevel — an origin-less local repo; the toplevel path makes
 *      distinct repos distinct.
 * Tabs that lack both fall through to the ungrouped bucket.
 */
function repoKeyAndLabel(tab: Tab): { key: string, label: string } | null {
  if (tab.gitOriginUrl)
    return { key: tab.gitOriginUrl, label: formatGitOriginUrl(tab.gitOriginUrl) }
  if (tab.gitToplevel) {
    const label = basename(tab.gitToplevel) || tab.gitToplevel
    return { key: `${LOCAL_REPO_KEY_PREFIX}${tab.gitToplevel}`, label }
  }
  return null
}

export function buildTree(tabs: Tab[]): TabTree {
  const ungrouped: Tab[] = []
  // Group by repo-key -> branch, preserving a stable display label per key.
  const repoMap = new Map<string, { label: string, branches: Map<string, Tab[]> }>()

  // A tab belongs under Repo → Branch when we can compute a repo key from
  // its git info (origin URL, toplevel, or as a last resort just a branch).
  // Tabs with none of those (non-git dirs) stay ungrouped.
  for (const tab of tabs) {
    const rk = repoKeyAndLabel(tab)
    if (!rk) {
      ungrouped.push(tab)
      continue
    }
    let entry = repoMap.get(rk.key)
    if (!entry) {
      entry = { label: rk.label, branches: new Map() }
      repoMap.set(rk.key, entry)
    }
    const branch = tab.gitBranch || '(no branch)'
    if (!entry.branches.has(branch))
      entry.branches.set(branch, [])
    entry.branches.get(branch)!.push(tab)
  }

  // Sort rule: real remotes first (alphabetical by formatted label), then
  // per-toplevel local repos (alphabetical by basename).
  const localRank = (key: string): number =>
    key.startsWith(LOCAL_REPO_KEY_PREFIX) ? 1 : 0

  const groups: RepoGroup[] = [...repoMap.entries()].toSorted(([aKey, a], [bKey, b]) => {
    const aRank = localRank(aKey)
    const bRank = localRank(bKey)
    if (aRank !== bRank)
      return aRank - bRank
    return a.label.localeCompare(b.label)
  }).map(([key, entry]) => {
    const branches = [...entry.branches.entries()].toSorted(([a], [b]) => a.localeCompare(b)).map(([branchName, branchTabs]) => {
      // All tabs on the same branch share the same git state, so use
      // the first tab that has diff stats rather than summing.
      let diffAdded = 0
      let diffDeleted = 0
      let diffUntracked = 0
      for (const t of branchTabs) {
        if ((t.gitDiffAdded ?? 0) > 0 || (t.gitDiffDeleted ?? 0) > 0 || (t.gitDiffUntracked ?? 0) > 0) {
          diffAdded = t.gitDiffAdded ?? 0
          diffDeleted = t.gitDiffDeleted ?? 0
          diffUntracked = t.gitDiffUntracked ?? 0
          break
        }
      }
      return { branchName, tabs: sortTabs(branchTabs), diffAdded, diffDeleted, diffUntracked }
    })
    return {
      repoKey: key,
      repoLabel: entry.label,
      branches,
      diffAdded: branches.reduce((sum, b) => sum + b.diffAdded, 0),
      diffDeleted: branches.reduce((sum, b) => sum + b.diffDeleted, 0),
      diffUntracked: branches.reduce((sum, b) => sum + b.diffUntracked, 0),
    }
  })

  return { groups, ungrouped: sortTabs(ungrouped) }
}

function sortTabs(tabs: Tab[]): Tab[] {
  return tabs.toSorted((a, b) => {
    // Agents before terminals
    if (a.type !== b.type)
      return a.type === TabType.AGENT ? -1 : 1
    // Then alphabetical by title
    return (a.title || a.id).localeCompare(b.title || b.id)
  })
}
