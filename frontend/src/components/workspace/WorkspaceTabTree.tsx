import type { Accessor, Component } from 'solid-js'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { Tab, TabItemOps } from '~/stores/tab.types'
import { createDraggable } from '@thisbeyond/solid-dnd'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FolderGit from 'lucide-solid/icons/folder-git'
import GitBranch from 'lucide-solid/icons/git-branch'
import X from 'lucide-solid/icons/x'
import { createContext, createMemo, createSignal, For, on, Show, useContext } from 'solid-js'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { TabTypeIcon } from '~/components/common/TabTypeIcon'
import { Tooltip } from '~/components/common/Tooltip'
import { SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { PREFIX_TAB_TREE, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { basename, flavorFromOs, tildify } from '~/lib/paths'
import { shallowEqualArrays } from '~/lib/shallowEqual'
import { diffStatsFromTabFields } from '~/stores/gitFileStatus.store'
import { canCloseTab, tabDisplayLabel, tabKey } from '~/stores/tab.helpers'
import { isTerminalTab } from '~/stores/tab.types'
import { terminalStatusClassList } from '../shell/terminalStatus'
import { RowLabelWithStats } from '../tree/gitStatusUtils'
import * as shared from '../tree/sharedTree.css'
import { menuTrigger, sidebarActions } from '../tree/sidebarActions.css'
import { BranchContextMenu } from './BranchContextMenu'
import {
  branchKey,
  branchNameSegment,
  collapseKeyForBranch,
  isLocalRepoKey,
  repoKeyForLocal,
  repoKeyTooltip,
} from './branchKeys'
import * as css from './workspaceTabTree.css'

/**
 * Display fallback for tabs whose git state has no branch name yet
 * (e.g. detached HEAD or a freshly-initialised repo). Rendered only at
 * the display layer — internally a missing branch is represented as
 * `null` so it can never collide with a real branch literally named
 * `(no branch)`.
 */
const NO_BRANCH_LABEL = '(no branch)'

function tabBranchKey(tab: Tab): string {
  return branchKey(tab.gitBranch || null, tab.workerId ?? '', tab.gitToplevel ?? '')
}

function branchGroupKey(b: BranchGroup): string {
  return branchKey(b.branchName, b.workerId, b.gitToplevel)
}

// Compact per-tab fingerprint used by tabsProjection. Mirrors every field
// `buildTree` reads from a Tab, joined with `\0` so adjacent field
// boundaries are unambiguous: pathnames, branch names, ids, and origin
// URLs can all contain `|` but never a literal NUL byte, so two distinct
// (gitToplevel, gitOriginUrl) pairs can't share a fingerprint by sliding
// across the separator. The leading id keeps every fingerprint unique
// across tabs regardless of the other fields. Exported for unit tests to
// pin the field-coverage contract.
export function tabBuildKey(t: Tab): string {
  return [
    t.id,
    t.workerId ?? '',
    t.gitBranch ?? '',
    t.gitToplevel ?? '',
    t.gitIsWorktree ? '1' : '0',
    t.gitOriginUrl ?? '',
    t.gitDiffAdded ?? 0,
    t.gitDiffDeleted ?? 0,
    t.gitDiffUntracked ?? 0,
    t.tileId ?? '',
    t.position ?? '',
  ].join('\0')
}

function buildBranchRef(workspaceId: string, b: BranchGroup): BranchRef {
  return {
    workspaceId,
    workerId: b.workerId,
    gitToplevel: b.gitToplevel,
    isWorktree: b.isWorktree,
    branchName: b.branchName,
    tabs: b.tabs,
  }
}

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
    { title: tabDisplayLabel(props.tab), type: props.tab.type },
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
      data-terminal-status={isTerminalTab(props.tab) ? props.tab.status : undefined}
    >
      <div class={shared.chevronPlaceholder} />
      <TabTypeIcon tab={props.tab} class={css.tabIcon} />
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
        <Tooltip text={tabDisplayLabel(props.tab)} showWhen="clipped">
          <span
            class={css.tabLabel}
            classList={terminalStatusClassList(isTerminalTab(props.tab) ? props.tab.status : undefined)}
          >
            {tabDisplayLabel(props.tab)}
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

/**
 * Selection + structural state every row reads. Provided once by
 * WorkspaceTabTree; consumed via `useRowSelection`. Accessors are used
 * for the reactive prop fields (`workspaceId`, `readOnly`,
 * `activeTabKey`, `tabItemOps`) so they track the parent's props
 * without leaning on JSX getter trickery.
 */
interface RowSelectionContextValue {
  workspaceId: Accessor<string>
  readOnly: Accessor<boolean | undefined>
  activeTabKey: Accessor<string | null>
  tabItemOps: Accessor<TabItemOps | undefined>
  onTabClick: (type: TabType, id: string) => void
  canClose: (tab: Tab) => boolean
  isCollapsed: (key: string) => boolean
  toggleCollapsed: (key: string) => void
}

/**
 * Tab-rename editing state, scoped to TabLeafSlot. Lives in its own
 * context so the branch/repo rows don't pull editing dependencies into
 * their reactive graphs.
 */
interface RowEditingContextValue {
  editingTabKey: Accessor<string | null>
  editingValue: Accessor<string>
  setEditingValue: (v: string) => void
  startEditing: (tab: Tab) => void
  commitEdit: (tab: Tab) => void
  cancelEdit: () => void
}

/**
 * Branch-row callbacks (Change/Delete). Only BranchGroupRow consumes
 * these; nested rows ignore the context.
 */
interface BranchActionsContextValue {
  onChangeBranch?: (ref: BranchRef) => void
  onDeleteBranch?: (ref: BranchRef) => void
}

const RowSelectionContext = createContext<RowSelectionContextValue>()
const RowEditingContext = createContext<RowEditingContextValue>()
const BranchActionsContext = createContext<BranchActionsContextValue>({})

function useRowSelection(): RowSelectionContextValue {
  const ctx = useContext(RowSelectionContext)
  if (!ctx)
    throw new Error('RowSelectionContext used outside WorkspaceTabTree')
  return ctx
}

function useRowEditing(): RowEditingContextValue {
  const ctx = useContext(RowEditingContext)
  if (!ctx)
    throw new Error('RowEditingContext used outside WorkspaceTabTree')
  return ctx
}

function useBranchActions(): BranchActionsContextValue {
  return useContext(BranchActionsContext)!
}

// Renders one tab leaf row. Pure wrapper around TabLeaf that pulls the
// per-tab interaction state (editing, closing, active) out of the
// shared row contexts.
const TabLeafSlot: Component<{ tab: Tab, depth: number }> = (props) => {
  const sel = useRowSelection()
  const edit = useRowEditing()
  return (
    <TabLeaf
      tab={props.tab}
      workspaceId={sel.workspaceId()}
      depth={props.depth}
      isActive={tabKey(props.tab) === sel.activeTabKey()}
      isEditing={edit.editingTabKey() === tabKey(props.tab)}
      editingValue={edit.editingValue()}
      onClick={() => sel.onTabClick(props.tab.type, props.tab.id)}
      onDblClick={() => edit.startEditing(props.tab)}
      onClose={() => sel.tabItemOps()?.onClose?.(props.tab)}
      isClosing={sel.tabItemOps()?.closingKeys?.has(tabKey(props.tab))}
      canClose={sel.canClose(props.tab)}
      onEditInput={v => edit.setEditingValue(v)}
      onEditCommit={() => edit.commitEdit(props.tab)}
      onEditCancel={edit.cancelEdit}
    />
  )
}

// Renders one branch row inside a repo group: the header (chevron +
// label + diff badge + Change/Delete menu) and the collapsible list of
// tab leaves. `branch` is an Accessor so the parent's outer `<For>` can
// iterate stable string keys and look up the live branch by key — a
// rebuild that swaps branch identity must not unmount the row.
const BranchGroupRow: Component<{
  branch: Accessor<BranchGroup>
  repoKey: string
  branchKey: string
}> = (props) => {
  const sel = useRowSelection()
  const actions = useBranchActions()
  const branchStats = createMemo(() => diffStatsFromTabFields(props.branch()))
  const collapseKey = createMemo(() => collapseKeyForBranch(props.repoKey, props.branchKey))
  return (
    <>
      <div
        class={shared.node}
        style={{ 'padding-left': '36px' }}
        onClick={() => sel.toggleCollapsed(collapseKey())}
        data-testid="tab-tree-branch-group"
      >
        <ChevronRight
          size={14}
          class={`${shared.chevron} ${!sel.isCollapsed(collapseKey()) ? shared.chevronExpanded : ''}`}
        />
        <GitBranch size={14} class={css.groupIcon} />
        <RowLabelWithStats
          label={props.branch().displayLabel}
          stats={branchStats()}
        />
        {/* Hide the Change/Delete menu on the synthetic "(no branch)"
            group: branchName=null means the tab is on detached HEAD or
            an unborn ref, and both actions would either fail in the
            worker (`git branch -D <short-sha>`) or have no meaningful
            target. Keeping the menu hidden is clearer than letting the
            user click into an error. */}
        {/* gitToplevel guard: tabs that haven't been git-stamped (initial
            paint after worker spawn, FILE tab restored from CRDT before
            the hydrator runs) carry an empty `BranchGroup.gitToplevel`.
            Exposing Change/Delete on those would send `path: ""` to the
            worker — SanitizePath rejects empty, so the dialog opens
            stuck on a permission-denied banner. Hide the menu until the
            row has a real repo identity. */}
        <Show when={!sel.readOnly() && props.branch().branchName !== null && props.branch().gitToplevel !== '' && actions.onChangeBranch && actions.onDeleteBranch}>
          <div class={sidebarActions}>
            <BranchContextMenu
              onChangeBranch={() => actions.onChangeBranch!(buildBranchRef(sel.workspaceId(), props.branch()))}
              onDeleteBranch={() => actions.onDeleteBranch!(buildBranchRef(sel.workspaceId(), props.branch()))}
            />
          </div>
        </Show>
      </div>

      <div class={`${shared.childrenWrapper} ${!sel.isCollapsed(collapseKey()) ? shared.childrenWrapperExpanded : ''}`}>
        <div class={shared.childrenInner}>
          <For each={props.branch().tabs}>
            {tab => <TabLeafSlot tab={tab} depth={3} />}
          </For>
        </div>
      </div>
    </>
  )
}

// Renders one repo group: the header (chevron + repo label + diff
// badge) and the collapsible list of branch rows. The branch list is
// iterated by stable composite key so a sibling branch's update doesn't
// unmount every row in the repo.
const RepoGroupRow: Component<{
  group: Accessor<RepoGroup>
  repoKey: string
}> = (props) => {
  const sel = useRowSelection()
  const groupStats = createMemo(() => diffStatsFromTabFields(props.group()))
  const branchKeys = createMemo(
    () => props.group().branches.map(branchGroupKey),
    [],
    { equals: shallowEqualArrays },
  )
  return (
    <>
      <div
        class={shared.node}
        style={{ 'padding-left': '20px' }}
        onClick={() => sel.toggleCollapsed(props.repoKey)}
        data-testid="tab-tree-repo-group"
      >
        <ChevronRight
          size={14}
          class={`${shared.chevron} ${!sel.isCollapsed(props.repoKey) ? shared.chevronExpanded : ''}`}
        />
        <FolderGit size={14} class={css.groupIcon} />
        <RowLabelWithStats
          label={props.group().repoLabel}
          tooltipLabel={repoKeyTooltip(props.repoKey)}
          stats={groupStats()}
        />
      </div>

      <div class={`${shared.childrenWrapper} ${!sel.isCollapsed(props.repoKey) ? shared.childrenWrapperExpanded : ''}`}>
        <div class={shared.childrenInner}>
          <For each={branchKeys()}>
            {(bKey) => {
              // Use Show to drop the row when the parent rebuilds the
              // group without this bKey before the For has had a chance
              // to reconcile the key list. Without it, a non-null
              // assertion would mask the undefined and the next
              // `props.branch()` read in BranchGroupRow would crash.
              const branch = createMemo(() => props.group().branchByKey.get(bKey))
              return (
                <Show when={branch()}>
                  {b => (
                    <BranchGroupRow
                      branch={b}
                      repoKey={props.repoKey}
                      branchKey={bKey}
                    />
                  )}
                </Show>
              )
            }}
          </For>
        </div>
      </div>
    </>
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
  /**
   * Tile ids in their top-left-first traversal order of the workspace's
   * layout tree. Drives the per-branch sort: leaves appear in the same
   * order as their tiles do visually, ties broken by LexoRank `position`
   * (the tab bar's left-to-right order). Omit (or pass `[]`) and the
   * sort falls back to type → title.
   */
  tileOrder?: string[]
  /**
   * Reactive lookup for worker display info. Used to disambiguate same-
   * named branches across distinct workers / clones (appending
   * `(worker-a)` or `(~/path)` to the branch label). When omitted, the
   * raw `workerId` and absolute toplevel path are used as fallbacks.
   */
  workerInfoFn?: (id: string) => WorkerInfo | null
  onChangeBranch?: (ref: BranchRef) => void
  onDeleteBranch?: (ref: BranchRef) => void
}

/**
 * Identifies a branch row for both the Change Branch and Delete Branch
 * dialogs. The two dialogs read overlapping subsets — Change reads
 * `workspaceId` + branch identity; Delete reads branch identity + tab
 * snapshot — and ignore the rest. A merged ref keeps the call site
 * simple (one shape, populated once from the branch row).
 *
 * `branchName` is `null` when the row groups tabs that have no current
 * branch (the sidebar's "(no branch)" bucket).
 */
export interface BranchRef {
  workspaceId: string
  workerId: string
  gitToplevel: string
  /**
   * True iff `gitToplevel` resolves to a linked worktree. Threaded to
   * ChangeBranchDialog so it can seed `isWorktreeRoot`/`isRepoRoot`
   * correctly before the inspect RPC lands — without this a worktree-
   * opened dialog briefly paints a main-repo shape and downstream
   * GitOptions memos compute against the wrong fields.
   */
  isWorktree: boolean
  branchName: string | null
  tabs: Tab[]
}

export const WorkspaceTabTree: Component<WorkspaceTabTreeProps> = (props) => {
  // Project the buildTree inputs into stable signals — each memo's
  // custom `equals` short-circuits when the projection's contents are
  // unchanged so a WatchEvents push that mutates unrelated tab fields
  // (title, runtime status, scroll state) doesn't rerun buildTree.
  //
  // One fingerprint string per tab: cheaper to compare element-for-element
  // than the 10-field flat tuple it replaced. A pipe-delimited shape keeps
  // each field's contribution unambiguous (an empty branch can't be
  // confused with a numeric diff value).
  const tabsProjection = createMemo<readonly string[]>(
    () => props.tabs.map(tabBuildKey),
    [],
    { equals: shallowEqualArrays },
  )
  const tileOrderProjection = createMemo<readonly string[]>(
    () => props.tileOrder ?? [],
    [],
    { equals: shallowEqualArrays },
  )
  // workerInfoFn affects the display label of cross-worker branches and
  // the sort within a branch; project by every worker id referenced in
  // the tabs, mapped through the lookup. When zero or one distinct
  // worker is referenced, every branch's `workerCount` collapses to ≤ 1
  // — `computeBranchDisplayLabel` never reads the worker name in that
  // regime, so we can skip both the sort and the per-id lookup and
  // return a stable empty array (the common single-worker case).
  const workersProjection = createMemo<readonly string[]>(
    () => {
      const fn = props.workerInfoFn
      if (!fn)
        return []
      const ids = new Set<string>()
      for (const t of props.tabs) {
        if (t.workerId)
          ids.add(t.workerId)
      }
      if (ids.size <= 1)
        return []
      const out: string[] = []
      for (const id of [...ids].sort()) {
        out.push(id, fn(id)?.name ?? '')
      }
      return out
    },
    [],
    { equals: shallowEqualArrays },
  )
  // buildTree re-runs only when one of the three projections changes.
  // Each projection memo keeps its previous array reference when the
  // contents are unchanged (via shallowEqualArrays), so `on()` sees
  // stable identity on no-op pushes.
  const tree = createMemo(
    on(
      () => [tabsProjection(), tileOrderProjection(), workersProjection()] as const,
      () => buildTree(props.tabs, props.tileOrder, props.workerInfoFn),
    ),
  )
  // Outer For iterates stable repoKey strings (interned by JS, so a fresh
  // array of equal-value strings reconciles row-for-row). Combined with
  // the per-row `group()` memo below, only the affected group's stats /
  // collapse classes rerun when one branch inside changes — neighbouring
  // group rows stay mounted across every WatchEvents push that updates a
  // single tab's git fields.
  //
  // `equals: shallowEqualArrays` short-circuits when a WatchEvents push
  // rebuilds the tree but leaves the key set unchanged (the common case
  // for diff-stat / branch-name updates). Without it, the `<For>` below
  // would reconcile every row on every push.
  const groupKeys = createMemo(
    () => tree().groups.map(g => g.repoKey),
    [],
    { equals: shallowEqualArrays },
  )
  const groupByKey = createMemo(() => {
    const m = new Map<string, RepoGroup>()
    for (const g of tree().groups) m.set(g.repoKey, g)
    return m
  })
  const storageKey = () => `${PREFIX_TAB_TREE}${props.workspaceId}`

  // --- Tab rename editing state ---
  const [editingTabKey, setEditingTabKey] = createSignal<string | null>(null)
  const [editingValue, setEditingValue] = createSignal('')
  let editCancelled = false
  const canClose = (tab: Tab) => canCloseTab(props.readOnly, tab)

  const startEditing = (tab: Tab) => {
    if (props.readOnly || tab.type === TabType.FILE || !props.tabItemOps?.onRename)
      return
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
      props.tabItemOps?.onRename?.(tab, value)
    }
    setEditingTabKey(null)
  }

  const cancelEdit = () => {
    editCancelled = true
    setEditingTabKey(null)
  }

  function loadCollapsedState(): Record<string, boolean> {
    return sessionStorageGet<Record<string, boolean>>(storageKey()) ?? {}
  }

  // Collapse state keyed by group label
  const [collapsed, setCollapsed] = createSignal<Record<string, boolean>>(loadCollapsedState())

  function isCollapsed(key: string): boolean {
    return collapsed()[key] ?? false
  }

  function toggleCollapsed(key: string) {
    setCollapsed((prev) => {
      const next = { ...prev, [key]: !prev[key] }
      sessionStorageSet(storageKey(), next)
      return next
    })
  }

  const selection: RowSelectionContextValue = {
    workspaceId: () => props.workspaceId,
    readOnly: () => props.readOnly,
    activeTabKey: () => props.activeTabKey,
    tabItemOps: () => props.tabItemOps,
    onTabClick: (type, id) => props.onTabClick(type, id),
    canClose,
    isCollapsed,
    toggleCollapsed,
  }
  const editing: RowEditingContextValue = {
    editingTabKey,
    editingValue,
    setEditingValue,
    startEditing,
    commitEdit,
    cancelEdit,
  }
  const actions: BranchActionsContextValue = {
    get onChangeBranch() {
      return props.onChangeBranch
    },
    get onDeleteBranch() {
      return props.onDeleteBranch
    },
  }

  return (
    <RowSelectionContext.Provider value={selection}>
      <RowEditingContext.Provider value={editing}>
        <BranchActionsContext.Provider value={actions}>
          <div class={css.treeWrapper} data-testid="workspace-tab-tree">
            <For each={groupKeys()}>
              {(repoKey) => {
                // Look up the live group from the memo'd map. A
                // WatchEvents push that updates a tab's git fields reruns
                // buildTree and re-emits this map, but the `For` row
                // stays mounted because repoKey is a stable string value.
                // Show drops the row when the parent rebuilds the map
                // without this repoKey before the For has reconciled —
                // a non-null assertion would let RepoGroupRow read
                // through `undefined` until the next reconciliation tick.
                const group = createMemo(() => groupByKey().get(repoKey))
                return (
                  <Show when={group()}>
                    {g => <RepoGroupRow group={g} repoKey={repoKey} />}
                  </Show>
                )
              }}
            </For>

            {/* Ungrouped tabs (no git info) */}
            <For each={tree().ungrouped}>
              {tab => <TabLeafSlot tab={tab} depth={1} />}
            </For>
          </div>
        </BranchActionsContext.Provider>
      </RowEditingContext.Provider>
    </RowSelectionContext.Provider>
  )
}

// --- Grouping logic ---

export interface BranchGroup {
  /**
   * Real branch name, or `null` for tabs without a branch yet. The
   * display layer renders `null` as `NO_BRANCH_LABEL`.
   */
  branchName: string | null
  /**
   * Worker that owns the tabs in this group. Tabs in different workers
   * land in separate groups even when their gitOriginUrl matches and the
   * branch name is the same.
   */
  workerId: string
  /** Working-tree root of the tabs in this group (resolved per worker). */
  gitToplevel: string
  /**
   * True iff this group's gitToplevel resolves to a linked worktree.
   * Lifted from any tab in the group — all tabs in a `(workerId,
   * gitToplevel)` bucket share the same worker view of the same path,
   * so the disposition is uniform. ChangeBranchDialog reads this to
   * seed its path-info shape before the inspect RPC lands.
   */
  isWorktree: boolean
  /**
   * Branch label shown in the row. Equal to `branchName` when this is
   * the only group with that name within its repo; otherwise suffixed
   * with `(worker)`, `(~/path)`, or `(worker, ~/path)` depending on
   * which dimensions vary between the colliding groups.
   */
  displayLabel: string
  tabs: Tab[]
  diffAdded: number
  diffDeleted: number
  diffUntracked: number
}

interface RepoGroup {
  repoKey: string
  repoLabel: string
  branches: BranchGroup[]
  /**
   * Per-row lookup map keyed by `branchKey(branchName, workerId, gitToplevel)`.
   * Built once during `buildTree` so each row's `<For>` body doesn't have to
   * rebuild its own Map on every reactive tick.
   */
  branchByKey: Map<string, BranchGroup>
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
    return { key: repoKeyForLocal(tab.gitToplevel), label }
  }
  return null
}

/**
 * Sum diff stats across the branch-groups a tab list would form, without
 * the full buildTree machinery. `buildTree` derives per-branch diff stats
 * by taking the first tab with non-zero stats in each `(branchName, workerId,
 * gitToplevel)` bucket (every tab in a bucket shares the same git state),
 * then sums those across branches. Callers that only need the workspace's
 * top-line diff badge can use this helper instead of allocating the full
 * group/branch structure.
 */
export function sumDiffStatsFromTabs(tabs: Tab[]): { added: number, deleted: number, untracked: number } {
  const seen = new Set<string>()
  let added = 0
  let deleted = 0
  let untracked = 0
  for (const t of tabs) {
    if (!t.gitOriginUrl && !t.gitToplevel)
      continue
    const a = t.gitDiffAdded ?? 0
    const d = t.gitDiffDeleted ?? 0
    const u = t.gitDiffUntracked ?? 0
    if (a === 0 && d === 0 && u === 0)
      continue
    const key = tabBranchKey(t)
    if (seen.has(key))
      continue
    seen.add(key)
    added += a
    deleted += d
    untracked += u
  }
  return { added, deleted, untracked }
}

export function buildTree(
  tabs: Tab[],
  tileOrder?: readonly string[],
  workerInfoFn?: (id: string) => WorkerInfo | null,
): TabTree {
  // Per-branch sort needs O(1) tile-index lookup; build the map once
  // here and reuse for every branch / the ungrouped bucket.
  const tileIndex = new Map<string, number>()
  if (tileOrder) {
    for (let i = 0; i < tileOrder.length; i++)
      tileIndex.set(tileOrder[i], i)
  }
  const sort = (xs: Tab[]) => sortTabs(xs, tileIndex)

  const ungrouped: Tab[] = []
  // Group by repo-key -> composite-branch-key. The composite key joins
  // branchName + workerId + gitToplevel so two clones of the same repo
  // (different workers OR different paths on the same worker) on the
  // same branch land in separate groups.
  const repoMap = new Map<string, {
    label: string
    branches: Map<string, { branchName: string | null, workerId: string, gitToplevel: string, isWorktree: boolean, tabs: Tab[] }>
  }>()

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
    const branchName = tab.gitBranch || null
    const workerId = tab.workerId ?? ''
    const gitToplevel = tab.gitToplevel ?? ''
    const key = branchKey(branchName, workerId, gitToplevel)
    let bucket = entry.branches.get(key)
    if (!bucket) {
      // Tabs are bucketed by (branchName, workerId, gitToplevel), so
      // every tab in a bucket shares the same worker view of the same
      // path — `gitIsWorktree` is uniform across the bucket. Seed it
      // from the first tab; later tabs that happen to disagree (e.g.
      // a stale broadcast races a probe refresh) leave the seed as-is
      // rather than flickering the group's worktree flag.
      bucket = { branchName, workerId, gitToplevel, isWorktree: tab.gitIsWorktree ?? false, tabs: [] }
      entry.branches.set(key, bucket)
    }
    bucket.tabs.push(tab)
  }

  // Sort rule: real remotes first (alphabetical by formatted label), then
  // per-toplevel local repos (alphabetical by basename).
  const localRank = (key: string): number => isLocalRepoKey(key) ? 1 : 0

  const groups: RepoGroup[] = [...repoMap.entries()].toSorted(([aKey, a], [bKey, b]) => {
    const aRank = localRank(aKey)
    const bRank = localRank(bKey)
    if (aRank !== bRank)
      return aRank - bRank
    return a.label.localeCompare(b.label)
  }).map(([key, entry]) => {
    // First pass: count branches by name. Most branch names appear
    // exactly once (no collision) — those don't need Sets at all and
    // skip the second pass entirely. `branchNameSegment` maps the
    // `null` (no-branch) bucket to a sentinel so it never collides
    // with a real branch literally named "(no branch)".
    const nameCount = new Map<string, number>()
    for (const b of entry.branches.values()) {
      const k = branchNameSegment(b.branchName)
      nameCount.set(k, (nameCount.get(k) ?? 0) + 1)
    }
    // Second pass: allocate Sets only for collision-prone branch names.
    // Lookups against this map default to "size 1" when absent, since a
    // missing entry means the branch is unique within its repo.
    const byBranchKey = new Map<string, {
      workerIds: Set<string>
      toplevels: Set<string>
    }>()
    for (const b of entry.branches.values()) {
      const k = branchNameSegment(b.branchName)
      if ((nameCount.get(k) ?? 0) < 2)
        continue
      let stats = byBranchKey.get(k)
      if (!stats) {
        stats = { workerIds: new Set(), toplevels: new Set() }
        byBranchKey.set(k, stats)
      }
      stats.workerIds.add(b.workerId)
      stats.toplevels.add(b.gitToplevel)
    }

    // Sort: null (no branch) last, then alphabetical by branch name.
    // Within ties: worker name then toplevel path.
    const branches = [...entry.branches.values()].toSorted((a, b) => {
      if (a.branchName === null && b.branchName !== null)
        return 1
      if (a.branchName !== null && b.branchName === null)
        return -1
      if (a.branchName !== null && b.branchName !== null) {
        const c = a.branchName.localeCompare(b.branchName)
        if (c !== 0)
          return c
      }
      const aw = workerInfoFn?.(a.workerId)?.name ?? a.workerId
      const bw = workerInfoFn?.(b.workerId)?.name ?? b.workerId
      const wc = aw.localeCompare(bw)
      if (wc !== 0)
        return wc
      return a.gitToplevel.localeCompare(b.gitToplevel)
    }).map(({ branchName, workerId, gitToplevel, isWorktree, tabs: branchTabs }) => {
      // All tabs in the same branch group share the same git state, so use
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
      const stats = byBranchKey.get(branchNameSegment(branchName))
      const displayLabel = computeBranchDisplayLabel(
        branchName,
        workerId,
        gitToplevel,
        stats?.workerIds.size ?? 1,
        stats?.toplevels.size ?? 1,
        workerInfoFn,
      )
      return {
        branchName,
        workerId,
        gitToplevel,
        isWorktree,
        displayLabel,
        tabs: sort(branchTabs),
        diffAdded,
        diffDeleted,
        diffUntracked,
      }
    })
    let groupAdded = 0
    let groupDeleted = 0
    let groupUntracked = 0
    const branchByKey = new Map<string, BranchGroup>()
    for (const b of branches) {
      groupAdded += b.diffAdded
      groupDeleted += b.diffDeleted
      groupUntracked += b.diffUntracked
      branchByKey.set(branchKey(b.branchName, b.workerId, b.gitToplevel), b)
    }
    return {
      repoKey: key,
      repoLabel: entry.label,
      branches,
      branchByKey,
      diffAdded: groupAdded,
      diffDeleted: groupDeleted,
      diffUntracked: groupUntracked,
    }
  })

  return { groups, ungrouped: sort(ungrouped) }
}

/**
 * Build the visible branch label, appending disambiguating context only
 * when the same branch name appears in more than one group inside the
 * same repo. `workerCount` and `toplevelCount` are computed across the
 * colliding groups; their value tells us which dimensions are ambiguous
 * (and therefore should appear in the suffix).
 */
function computeBranchDisplayLabel(
  branchName: string | null,
  workerId: string,
  gitToplevel: string,
  workerCount: number,
  toplevelCount: number,
  workerInfoFn?: (id: string) => WorkerInfo | null,
): string {
  const labelBase = branchName === null ? NO_BRANCH_LABEL : branchName
  if (workerCount <= 1 && toplevelCount <= 1)
    return labelBase
  const info = workerInfoFn?.(workerId)
  const parts: string[] = []
  if (workerCount > 1) {
    const name = info?.name
    parts.push(name && name.length > 0 ? name : workerId)
  }
  if (toplevelCount > 1) {
    const homeDir = info?.homeDir
    parts.push(homeDir ? tildify(gitToplevel, homeDir, flavorFromOs(info?.os)) : gitToplevel)
  }
  if (parts.length === 0)
    return labelBase
  return `${labelBase} (${parts.join(', ')})`
}

/**
 * Order tabs by their visual position in the workspace. Primary key is
 * the tab's tile in `tileIndex` (top-left tile first; the index is built
 * from `getAllTileIds(root)` upstream). Within the same tile, fall back
 * to LexoRank `position` so the sidebar tracks the tab bar's left-to-
 * right order. Tabs whose tile is absent from `tileIndex` (no `tileId`
 * yet, or a layout/snapshot race) sink to the bottom but stay grouped
 * together by tile; `id` is the final, stable tiebreak.
 *
 * When `tileIndex` is empty (no tile order supplied — e.g. a test
 * harness or a workspace whose layout hasn't been hydrated yet) every
 * tab gets the same primary rank, so the sort effectively becomes
 * position-then-id. That keeps callers without layout info from
 * producing arbitrary orderings.
 */
function sortTabs(tabs: Tab[], tileIndex: Map<string, number>): Tab[] {
  const rank = (tileId: string | undefined): number => {
    if (!tileId)
      return Number.POSITIVE_INFINITY
    const idx = tileIndex.get(tileId)
    return idx === undefined ? Number.POSITIVE_INFINITY : idx
  }
  return tabs.toSorted((a, b) => {
    const ra = rank(a.tileId)
    const rb = rank(b.tileId)
    if (ra !== rb)
      return ra - rb
    const pa = a.position ?? ''
    const pb = b.position ?? ''
    if (pa !== pb)
      return pa < pb ? -1 : 1
    return a.id.localeCompare(b.id)
  })
}
