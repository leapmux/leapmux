import type { Component } from 'solid-js'
import type { Tab } from '~/stores/tab.store'
import Bot from 'lucide-solid/icons/bot'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FolderGit from 'lucide-solid/icons/folder-git'
import GitBranch from 'lucide-solid/icons/git-branch'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { tabKey, TabType } from '~/stores/tab.store'
import { DiffStatsBadge } from '../tree/gitStatusUtils'
import * as shared from '../tree/sharedTree.css'
import * as css from './workspaceTabTree.css'

// --- Tab leaf node ---

const TabLeaf: Component<{
  tab: Tab
  depth: number
  isActive: boolean
  onClick: () => void
}> = (props) => {
  return (
    <div
      class={`${shared.node} ${css.leafNode} ${props.isActive ? css.leafActive : ''}`}
      style={{ 'padding-left': `${8 + props.depth * 16}px` }}
      onClick={() => props.onClick()}
      data-testid="tab-tree-leaf"
    >
      <div class={shared.chevronPlaceholder} />
      <Show when={props.tab.type === TabType.AGENT} fallback={<Terminal size={14} class={css.tabIcon} />}>
        <Bot size={14} class={css.tabIcon} />
      </Show>
      <span class={css.tabLabel}>
        {props.tab.title || props.tab.id}
      </span>
    </div>
  )
}

// --- Public API ---

export interface WorkspaceTabTreeProps {
  tabs: Tab[]
  activeTabKey: string | null
  onTabClick: (type: TabType, id: string) => void
  workspaceId: string
}

export const WorkspaceTabTree: Component<WorkspaceTabTreeProps> = (props) => {
  const tree = createMemo(() => buildTree(props.tabs))
  const storageKey = () => `leapmux:tabTree:${props.workspaceId}`

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
                style={{ 'padding-left': '24px' }}
                onClick={() => toggleCollapsed(group.repoKey)}
                data-testid="tab-tree-repo-group"
              >
                <ChevronRight
                  size={14}
                  class={`${shared.chevron} ${!isCollapsed(group.repoKey) ? shared.chevronExpanded : ''}`}
                />
                <FolderGit size={14} class={css.groupIcon} />
                <span class={css.groupLabel}>{group.repoLabel}</span>
                <DiffStatsBadge added={group.diffAdded} deleted={group.diffDeleted} />
              </div>

              <div class={`${shared.childrenWrapper} ${!isCollapsed(group.repoKey) ? shared.childrenWrapperExpanded : ''}`}>
                <div class={shared.childrenInner}>
                  <For each={group.branches}>
                    {branch => (
                      <>
                        {/* Branch group header */}
                        <div
                          class={shared.node}
                          style={{ 'padding-left': '40px' }}
                          onClick={() => toggleCollapsed(`${group.repoKey}:${branch.branchName}`)}
                          data-testid="tab-tree-branch-group"
                        >
                          <ChevronRight
                            size={14}
                            class={`${shared.chevron} ${!isCollapsed(`${group.repoKey}:${branch.branchName}`) ? shared.chevronExpanded : ''}`}
                          />
                          <GitBranch size={14} class={css.groupIcon} />
                          <span class={css.groupLabel}>{branch.branchName}</span>
                          <DiffStatsBadge added={branch.diffAdded} deleted={branch.diffDeleted} />
                        </div>

                        <div class={`${shared.childrenWrapper} ${!isCollapsed(`${group.repoKey}:${branch.branchName}`) ? shared.childrenWrapperExpanded : ''}`}>
                          <div class={shared.childrenInner}>
                            <For each={branch.tabs}>
                              {tab => (
                                <TabLeaf
                                  tab={tab}
                                  depth={3}
                                  isActive={tabKey(tab) === props.activeTabKey}
                                  onClick={() => props.onTabClick(tab.type, tab.id)}
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
            depth={1}
            isActive={tabKey(tab) === props.activeTabKey}
            onClick={() => props.onTabClick(tab.type, tab.id)}
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
}

interface RepoGroup {
  repoKey: string
  repoLabel: string
  branches: BranchGroup[]
  diffAdded: number
  diffDeleted: number
}

interface TabTree {
  groups: RepoGroup[]
  ungrouped: Tab[]
}

export function formatGitOriginUrl(url: string): string {
  if (!url)
    return ''
  let result = url
  // Convert SSH format: git@github.com:org/repo -> github.com/org/repo
  const sshMatch = result.match(/^git@([^:]+):(.+)$/)
  if (sshMatch)
    result = `${sshMatch[1]}/${sshMatch[2]}`
  // Strip protocols
  result = result.replace(/^https?:\/\//, '')
  // Strip trailing .git
  result = result.replace(/\.git$/, '')
  // Strip trailing slash
  result = result.replace(/\/$/, '')
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
  const groups: RepoGroup[] = [...repoMap.entries()]
    .sort(([a], [b]) => formatGitOriginUrl(a).localeCompare(formatGitOriginUrl(b)))
    .map(([url, branchMap]) => {
      const branches = [...branchMap.entries()]
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([branchName, branchTabs]) => {
          // All tabs on the same branch share the same git state, so use
          // the first tab that has diff stats rather than summing.
          let diffAdded = 0
          let diffDeleted = 0
          for (const t of branchTabs) {
            if ((t.gitDiffAdded ?? 0) > 0 || (t.gitDiffDeleted ?? 0) > 0) {
              diffAdded = t.gitDiffAdded ?? 0
              diffDeleted = t.gitDiffDeleted ?? 0
              break
            }
          }
          return { branchName, tabs: sortTabs(branchTabs), diffAdded, diffDeleted }
        })
      return {
        repoKey: url,
        repoLabel: formatGitOriginUrl(url),
        branches,
        diffAdded: branches.reduce((sum, b) => sum + b.diffAdded, 0),
        diffDeleted: branches.reduce((sum, b) => sum + b.diffDeleted, 0),
      }
    })

  return { groups, ungrouped: sortTabs(ungrouped) }
}

function sortTabs(tabs: Tab[]): Tab[] {
  return [...tabs].sort((a, b) => {
    // Agents before terminals
    if (a.type !== b.type)
      return a.type === TabType.AGENT ? -1 : 1
    // Then alphabetical by title
    return (a.title || a.id).localeCompare(b.title || b.id)
  })
}
