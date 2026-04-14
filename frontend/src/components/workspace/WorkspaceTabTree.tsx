import type { Component } from 'solid-js'
import type { Tab } from '~/stores/tab.store'
import { createDraggable } from '@thisbeyond/solid-dnd'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FolderGit from 'lucide-solid/icons/folder-git'
import GitBranch from 'lucide-solid/icons/git-branch'
import Terminal from 'lucide-solid/icons/terminal'
import X from 'lucide-solid/icons/x'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { AgentProviderIcon } from '~/components/common/AgentProviderIcon'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { tabKey, TabType } from '~/stores/tab.store'
import { DiffStatsBadge } from '../tree/gitStatusUtils'
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
        <span class={css.tabLabel}>
          {props.tab.title || props.tab.id}
        </span>
      </Show>
      <Show when={props.canClose}>
        <div class={`${sidebarActions} ${css.leafActions}`}>
          <IconButton
            icon={X}
            iconSize="xs"
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
  onTabClose?: (tab: Tab) => void
  onTabRename?: (tab: Tab, title: string) => void
  closingTabKeys?: Set<string>
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
  const canClose = (tab: Tab) => !props.readOnly || tab.type === TabType.FILE

  const tabLabel = (tab: Tab): string => tab.title || tab.id

  const startEditing = (tab: Tab) => {
    if (props.readOnly || tab.type === TabType.FILE || !props.onTabRename)
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
      props.onTabRename?.(tab, value)
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
                <span class={css.groupLabel}>{group.repoLabel}</span>
                <DiffStatsBadge added={group.diffAdded} deleted={group.diffDeleted} untracked={group.diffUntracked} />
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
                          <span class={css.groupLabel}>{branch.branchName}</span>
                          <DiffStatsBadge added={branch.diffAdded} deleted={branch.diffDeleted} untracked={branch.diffUntracked} />
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
                                  onClose={() => props.onTabClose?.(tab)}
                                  isClosing={props.closingTabKeys?.has(tabKey(tab))}
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
            onClose={() => props.onTabClose?.(tab)}
            isClosing={props.closingTabKeys?.has(tabKey(tab))}
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

export function buildTree(tabs: Tab[]): TabTree {
  const grouped: Tab[] = []
  const ungrouped: Tab[] = []

  for (const tab of tabs) {
    if (tab.gitOriginUrl) {
      grouped.push(tab)
    }
    else {
      ungrouped.push(tab)
    }
  }

  // Group by origin URL -> branch
  const repoMap = new Map<string, Map<string, Tab[]>>()
  for (const tab of grouped) {
    const url = tab.gitOriginUrl!
    if (!repoMap.has(url))
      repoMap.set(url, new Map())
    const branchMap = repoMap.get(url)!
    const branch = tab.gitBranch || '(no branch)'
    if (!branchMap.has(branch))
      branchMap.set(branch, [])
    branchMap.get(branch)!.push(tab)
  }

  // Sort and build tree
  const groups: RepoGroup[] = [...repoMap.entries()].toSorted(([a], [b]) => formatGitOriginUrl(a).localeCompare(formatGitOriginUrl(b))).map(([url, branchMap]) => {
    const branches = [...branchMap.entries()].toSorted(([a], [b]) => a.localeCompare(b)).map(([branchName, branchTabs]) => {
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
      repoKey: url,
      repoLabel: formatGitOriginUrl(url),
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
