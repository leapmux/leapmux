import type { Accessor, Component } from 'solid-js'
import type { BranchRefActions } from './branchActions'
import type { PathFlavor } from '~/lib/paths'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { RepoGitStore } from '~/stores/repoGit'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { Tab, TabItemOps } from '~/stores/tab.types'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FolderGit from 'lucide-solid/icons/folder-git'
import X from 'lucide-solid/icons/x'
import { createMemo, createSignal, on, Show, useContext } from 'solid-js'
import { DragHandle } from '~/components/common/DragHandle'
import { createContextMenuAnchor } from '~/components/common/DropdownMenu'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { TabContextMenu } from '~/components/common/TabContextMenu'
import { TabTypeIcon } from '~/components/common/TabTypeIcon'
import { Tooltip } from '~/components/common/Tooltip'
import { WorkingTreeIcon, workingTreeKindLabel, WorkingTreeRows } from '~/components/common/WorkingTree'
import { SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { PREFIX_TAB_TREE, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { createStableContext } from '~/lib/createStableContext'
import { attachDragActivators } from '~/lib/dragActivators'
import { createGuardedDraggableRow } from '~/lib/dragRow'
import { createKeyedRows, createKeyLookup, createStableKeys, KeyedFor } from '~/lib/keyedRows'
import { basename, flavorFromOs, tildify } from '~/lib/paths'
import { shallowEqualArrays, shallowEqualExcept } from '~/lib/shallowEqual'
import { diffStatsFromRepo, repoGitView } from '~/stores/repoGit'
import { canCloseTab, canRenameTab, tabDisplayLabel, tabKey, tabTooltipShowWhen, tabTooltipText, terminalProgressBarProps, terminalProgressVisible } from '~/stores/tab.helpers'
import { isAgentTab, isTerminalTab } from '~/stores/tab.types'
import * as tabBarStyles from '../shell/TabBar.css'
import { terminalStatusClassList } from '../shell/terminalStatus'
import { RowLabelWithStats } from '../tree/gitStatusUtils'
import * as shared from '../tree/sharedTree.css'
import { menuTrigger, sidebarActions } from '../tree/sidebarActions.css'
import { bindBranchActions, WORKER_OFFLINE_BRANCH_REASON } from './branchActions'
import { BranchContextMenu } from './BranchContextMenu'
import {
  branchKey,
  branchNameSegment,
  collapseKeyForBranch,
  isLocalRepoKey,
  repoKeyForLocal,
  repoKeyTooltip,
  tabBranchKey,
  tabGitToplevelForKey,
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
//
// STRUCTURE ONLY. The fields here are the ones that decide which group a tab
// lands in, what order it sits in, and what the branch/repo diff badges add up
// to. Everything a ROW renders -- title, agent provider, terminal status, PTY
// title, progress -- is deliberately absent, and must never be added: those
// change at PTY-read and status-push frequency, and rebuilding the whole tree
// on each one is precisely what this gate exists to prevent. The rows read
// those fields from the live lookup instead (see `TabLeafList`), so a field
// missing here is not a field that goes stale.
export function tabBuildKey(t: Tab, store: RepoGitStore): string {
  const git = repoGitView(t, store)
  return [
    t.id,
    // `type` because the ROW key is `${type}:${id}` (tabKey), not the id alone.
    // Leaving it out lets the cached key list hold a key the live lookup can
    // never resolve if a tab's type ever changes in place -- and now that a
    // subagent row renders INSIDE its parent's guard, an unresolvable parent
    // takes its whole subtree out of the sidebar with it, not just one row.
    t.type,
    // Structure: a subagent tab renders UNDER its parent, so the link decides
    // where the row sits. It is written once (undefined -> id, at hydration),
    // so including it costs one rebuild per subagent rather than churn.
    isAgentTab(t) ? t.parentAgentId ?? '' : '',
    t.workerId ?? '',
    git.branchLabel ?? '',
    // The same resolution the tree itself uses, so the fingerprint matches
    // buildTree / tabBranchKey -- including when the store says "not a git
    // repository" and the row's stale toplevel must not win. The view is
    // already resolved above; passing it in saves the second resolution per
    // key build.
    tabGitToplevelForKey(t, store, git),
    git.isWorktree ? '1' : '0',
    git.originUrl ?? '',
    git.diffStats.added,
    git.diffStats.deleted,
    git.diffStats.untracked,
    t.tileId ?? '',
    t.position ?? '',
  ].join('\0')
}

/** One worker id and the info the tree resolved for it. */
export interface WorkerProjectionEntry {
  id: string
  info: WorkerInfo | null
}

/**
 * Equality for `workersProjection`.
 *
 * It compares EVERY `WorkerInfo` field except `updatedAt`, rather than the few
 * that `buildTree` happens to read. A read-list is a second source of truth:
 * add a field to `buildTree`, forget the list, and the tree freezes at that
 * field's pre-RPC value exactly as `homeDir` did. Comparing every field can
 * only ask for MORE rebuilds than the tree needs, never fewer, so a field the
 * tree ignores costs one extra rebuild when it changes — never a stale row.
 *
 * `updatedAt` is the one exclusion. The store stamps a fresh one on every TTL
 * probe and keeps the previous record when nothing else moved, so tracking it
 * would rebuild the whole tree on a no-op refetch.
 *
 * Per-FIELD comparison, not `Object.is` on the record: `workerInfoFn` is an
 * arbitrary prop, and a caller that allocates a fresh record per call — which
 * every test in this file does — would make an identity-based memo never
 * settle.
 */
export function workerProjectionsEqual(
  a: readonly WorkerProjectionEntry[],
  b: readonly WorkerProjectionEntry[],
): boolean {
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    if (a[i].id !== b[i].id)
      return false
    const x = a[i].info
    const y = b[i].info
    if (x === y)
      continue
    if (!x || !y)
      return false
    if (!shallowEqualExcept(x, y, ['updatedAt']))
      return false
  }
  return true
}

/**
 * Snapshot one branch row for the change and the delete dialog.
 *
 * `tabs` is re-resolved through the LIVE lookup rather than handed straight
 * from `b.tabs`: the branch group is a cached structure (see `tabBuildKey`), so
 * its own `Tab` objects can predate the last hydration or rename. Both dialogs
 * freeze what they get at open time -- `DeleteBranchDialog` counts tabs by type
 * and reads a `workingDir` off one of them -- so the snapshot they freeze had
 * better be the current one. A key that no longer resolves names a tab closed
 * since the last rebuild; dropping it is the point, not a loss.
 */
function buildBranchRef(workspaceId: string, b: BranchGroup, liveTab: (key: string) => Tab | undefined): BranchRef {
  return {
    workspaceId,
    workerId: b.workerId,
    gitToplevel: b.gitToplevel,
    isWorktree: b.isWorktree,
    branchName: b.branchName,
    tabs: b.tabs.map(t => liveTab(tabKey(t))).filter((t): t is Tab => t !== undefined),
  }
}

// --- Subagent nesting ---

/** One row of the sidebar's tab tree, plus the subagent rows hanging under it. */
export interface TabNode {
  tab: Tab
  children: TabNode[]
}

function parentAgentIdOf(tab: Tab): string | undefined {
  return isAgentTab(tab) ? tab.parentAgentId : undefined
}

/**
 * Fold a flat, already-sorted tab list into the subagent tree the sidebar draws.
 *
 * A child agent tab (one with `parentAgentId`) hangs under its parent when that
 * parent is in the SAME list; otherwise it stays at the top level, because the
 * parent tab is closed or sits in a different branch group and there is no row
 * to hang it from. Nesting is per direct parent only -- a subagent whose own
 * subagent is open renders two levels deep, but a tab whose parent is absent is
 * NOT re-parented onto a surviving grandparent, which would claim a lineage the
 * user cannot see.
 *
 * Order within every level is the input order, so the caller's sort still
 * decides it. Pure: it reads only the fields above and allocates a fresh forest.
 */
export function nestSubagentTabs(tabs: readonly Tab[]): TabNode[] {
  // Keyed by the composite tabKey, not by a bare id. `tabKey` is namespaced by
  // TYPE precisely because an AGENT and a TERMINAL tab can share an id, and a
  // parent link only ever names an AGENT -- so a bare-id lookup let a non-agent
  // tab resolve to the agent's node, push it into the forest twice, and drop the
  // non-agent row entirely.
  const agentNodeKey = (id: string) => tabKey({ type: TabType.AGENT, id } as Tab)
  const nodeByKey = new Map<string, TabNode>()
  for (const tab of tabs) {
    if (isAgentTab(tab))
      nodeByKey.set(tabKey(tab), { tab, children: [] })
  }

  // True when walking up from `from` reaches `targetId`. Guards against a
  // parent cycle: the worker cannot produce one (parent_agent_id is a DAG
  // rooted at a main agent), but attaching both ends of a cycle would recurse
  // forever in the renderer, so a suspect link demotes the tab to a root
  // instead. The visited set also limits a chain that repeats for any reason.
  const reaches = (from: TabNode, targetId: string): boolean => {
    const seen = new Set<string>()
    let id = parentAgentIdOf(from.tab)
    while (id && !seen.has(id)) {
      if (id === targetId)
        return true
      seen.add(id)
      const next = nodeByKey.get(agentNodeKey(id))
      id = next ? parentAgentIdOf(next.tab) : undefined
    }
    return false
  }

  const roots: TabNode[] = []
  for (const tab of tabs) {
    const node = nodeByKey.get(tabKey(tab)) ?? { tab, children: [] }
    const parentId = parentAgentIdOf(tab)
    const parent = parentId ? nodeByKey.get(agentNodeKey(parentId)) : undefined
    if (parent && parent !== node && !reaches(parent, tab.id))
      parent.children.push(node)
    else
      roots.push(node)
  }
  return roots
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
  /** Start the inline rename from the row's context menu. Undefined hides the item. */
  onRename?: () => void
  onClose?: () => void
  isClosing?: boolean
  canClose: boolean
  onEditInput: (value: string) => void
  onEditCommit: () => void
  onEditCancel: () => void
}> = (props) => {
  // The row element, for its right-click / long-press menu.
  const [rowEl, setRowEl] = createContextMenuAnchor()
  /* eslint-disable solid/reactivity -- stable identifier for the draggable row */
  const dragRow = createGuardedDraggableRow(
    `${SIDEBAR_TAB_PREFIX}${props.workspaceId}:${props.tab.type}:${props.tab.id}`,
    // `title` is a GETTER, not a snapshot. solid-dnd stores this object by
    // reference and `TabDragContext`'s overlay renderer reads it when a drag
    // starts, which can be long after the row mounted -- and the row now
    // survives every metadata-only change, which is the whole point of the live
    // lookup. A captured string would show the drag overlay the title the tab
    // held at mount: "Agent" for one whose real title arrived from hydration a
    // moment later, while the row beneath the cursor reads correctly.
    //
    // `type` stays a plain value: it is part of the draggable's own id above, so
    // a tab whose type changed would be a different row entirely.
    {
      get title() {
        return tabDisplayLabel(props.tab)
      },
      type: props.tab.type,
    },
  )
  /* eslint-enable solid/reactivity */
  // Mouse-only activation on the row body; the grip carries the raw handlers,
  // so touch drags start there and nowhere else.
  attachDragActivators(() => rowEl(), dragRow.bodyActivators, { touch: 'block' })

  return (
    <div
      ref={(el) => {
        setRowEl(el)
        // Node registration only — activation lives on the guarded body and
        // the grip; the transform arrives through the style prop below.
        dragRow.ref(el)
      }}
      class={`${shared.node} ${css.leafNode} ${props.isActive ? css.leafActive : ''} ${dragRow.isActiveDraggable ? css.leafDragging : ''}`}
      style={{
        'padding-left': `${4 + props.depth * 16}px`,
        ...dragRow.style(),
      }}
      onClick={() => {
        if (!dragRow.isActiveDraggable)
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
      // The tree's own statement of which row is active, so a test asserts on
      // that rather than on the presence of a hashed style class.
      data-active={props.isActive ? 'true' : 'false'}
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
        <Tooltip text={tabTooltipText(props.tab)} showWhen={tabTooltipShowWhen(props.tab)}>
          <span
            class={css.tabLabel}
            classList={terminalStatusClassList(isTerminalTab(props.tab) ? props.tab.status : undefined)}
          >
            {tabDisplayLabel(props.tab)}
          </span>
        </Tooltip>
      </Show>
      <Show when={terminalProgressVisible(props.tab)}>
        <span
          class={tabBarStyles.tabProgress}
          data-testid="tab-progress"
          {...terminalProgressBarProps(props.tab)}
        />
      </Show>
      <DragHandle activators={dragRow.gripActivators} testId="sidebar-tab-drag-handle" />
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
      {/* Outside the `canClose` block: a row that cannot be closed can still be
          renamed, and the menu host collapses to `display: contents`, so it costs
          the row no layout either way. */}
      <TabContextMenu
        contextMenuFor={rowEl}
        data-testid="tab-tree-leaf-menu"
        onRename={props.onRename}
        onClose={props.canClose ? props.onClose : undefined}
        isClosing={props.isClosing}
      />
    </div>
  )
}

/**
 * Selection + structural state every row reads. Provided once by
 * WorkspaceTabTree; consumed via `useRowSelection`. Accessors are used
 * for the reactive prop fields (`workspaceId`, `archived`,
 * `activeTabKey`, `tabItemOps`) so they track the parent's props
 * without leaning on JSX getter trickery.
 */
interface RowSelectionContextValue {
  workspaceId: Accessor<string>
  archived: Accessor<boolean | undefined>
  activeTabKey: Accessor<string | null>
  tabItemOps: Accessor<TabItemOps | undefined>
  onTabClick: (type: TabType, id: string) => void
  canClose: (tab: Tab) => boolean
  isCollapsed: (key: string) => boolean
  toggleCollapsed: (key: string) => void
  /**
   * The tab a key names RIGHT NOW, straight off `props.tabs` -- never off the
   * cached tree. Reactive: reading it inside a row subscribes that row to its
   * own tab, so a metadata-only change (a rename, a hydrated title/provider, a
   * terminal status flip) updates the row in place without rebuilding the tree.
   * Returns undefined for a tab closed since the last rebuild.
   */
  liveTab: (key: string) => Tab | undefined
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
  /**
   * Whether `startEditing` would do anything for this tab. `startEditing` returns
   * early on the same condition, so a caller that only needs to ACT can ignore
   * this; the row menu needs it to hide a Rename item that would do nothing.
   */
  canRename: (tab: Tab) => boolean
  startEditing: (tab: Tab) => void
  commitEdit: (tab: Tab) => void
  cancelEdit: () => void
}

/**
 * Branch-row callbacks. Only BranchGroupRow consumes these; nested rows
 * ignore the context.
 */
interface BranchActionsContextValue {
  branchActions?: BranchRefActions
  isWorkerKnownOnline?: (workerId: string) => boolean
}

const RowSelectionContext = createStableContext<RowSelectionContextValue>('workspace/WorkspaceTabTree#rowSelection')
const RowEditingContext = createStableContext<RowEditingContextValue>('workspace/WorkspaceTabTree#rowEditing')
const BranchActionsContext = createStableContext<BranchActionsContextValue>('workspace/WorkspaceTabTree#branchActions', {})

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
      onRename={edit.canRename(props.tab) ? () => edit.startEditing(props.tab) : undefined}
      onClose={() => sel.tabItemOps()?.onClose?.(props.tab)}
      isClosing={sel.tabItemOps()?.closingKeys?.has(tabKey(props.tab))}
      canClose={sel.canClose(props.tab)}
      onEditInput={v => edit.setEditingValue(v)}
      onEditCommit={() => edit.commitEdit(props.tab)}
      onEditCancel={edit.cancelEdit}
    />
  )
}

/**
 * A list of tab leaves keyed by TAB KEY, not by the `Tab` object.
 *
 * A `Tab` is a join result (see tabView) rebuilt whenever any field it derives
 * from `tabMetadata` changes -- a title rename, a git badge refresh, an agent
 * status flip, a notification dot, the MRU stamp another tab's activation
 * writes. `<For>` keys by item IDENTITY, so iterating the objects meant every
 * one of those disposed and re-created every row in the list. This list is
 * where that hurts most: a row can hold the inline rename `<input>`, and
 * remounting it mid-rename destroys the element the user is typing into,
 * dropping focus and the text with it.
 *
 * Keys are strings, so `shallowEqualArrays` means the `<For>` reconciles only
 * when a tab is actually added, removed, or reordered; every other field is read
 * reactively INSIDE the row, where Solid updates props in place. This mirrors
 * what `TileRenderer` and `TerminalView` do for the panes.
 *
 * The keys come from `props.tabs` (a bucket of the CACHED tree) and the items
 * from the LIVE lookup, and the split is the whole point. Order and membership
 * are decided by fields the tree's fingerprint covers, so taking them from the
 * cache is correct and is what keeps the rows from reconciling on every status
 * push. Everything a row RENDERS is not in that fingerprint, so resolving the
 * item from the cache too -- which is what pairing both halves of
 * `createKeyedRows` against `props.tabs` did -- froze each row's title, provider
 * icon, terminal status and progress at whatever they were when the tree last
 * rebuilt. A tab that reached the sidebar before its worker metadata (a peer
 * client's tab, a `leapmux control tab open`, a cold reload, or a hydration reply
 * that lands after the git fields have already settled) then kept the bare
 * "Agent" label and the generic bot icon until some unrelated tab forced a
 * rebuild.
 */
// Renders one level of the tab tree, then recurses into each row's subagents at
// one greater depth (TabLeaf turns depth into its indent).
const TabNodeList: Component<{ nodes: readonly TabNode[], depth: number }> = (props) => {
  const sel = useRowSelection()
  const keys = createStableKeys(() => props.nodes.map(n => n.tab), tabKey)
  // Children come from the CACHED tree (they are structure, like order and
  // membership); the row itself still resolves through the live lookup.
  const childrenByKey = createMemo(() => {
    const map = new Map<string, TabNode[]>()
    for (const n of props.nodes) {
      if (n.children.length > 0)
        map.set(tabKey(n.tab), n.children)
    }
    return map
  })
  return (
    <KeyedFor each={keys()} lookup={key => sel.liveTab(key)}>
      {(tab, key) => (
        <>
          <TabLeafSlot tab={tab()} depth={props.depth} />
          <Show when={childrenByKey().get(key)}>
            {children => <TabNodeList nodes={children()} depth={props.depth + 1} />}
          </Show>
        </>
      )}
    </KeyedFor>
  )
}

/**
 * The rows for one branch bucket: nests the flat tab list into a forest, then
 * hands it to TabNodeList, which owns the keying described above.
 */
const TabLeafList: Component<{ tabs: readonly Tab[], depth: number }> = (props) => {
  // Nest here rather than in buildTree: BranchGroup.tabs stays the flat set of
  // tabs ON the branch, which is what the diff-stat roll-up and the branch
  // dialogs' snapshot want. Only the sidebar draws a hierarchy. The memo's
  // input is the cached tree's array, so this recomputes only when buildTree
  // does -- parentAgentId is part of tabBuildKey, so a hydrated subagent link
  // rebuilds the tree and re-nests here.
  const nodes = createMemo(() => nestSubagentTabs(props.tabs))
  return <TabNodeList nodes={nodes()} depth={props.depth} />
}

// Renders one branch row inside a repo group: the header (chevron +
// label + diff badge + branch context menu) and the collapsible list of
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
  const branchStats = createMemo(() => diffStatsFromRepo(props.branch()))
  const collapseKey = createMemo(() => collapseKeyForBranch(props.repoKey, props.branchKey))
  // Every item of the menu needs this row's Worker: the change items read the
  // branch state from it, Delete mutates it, and the new-tab items start an
  // agent or a terminal on it. Undefined when they are usable -- see
  // BranchContextMenu.disabledReason.
  const menuDisabledReason = createMemo(() => {
    const isOnline = actions.isWorkerKnownOnline
    if (!isOnline || isOnline(props.branch().workerId))
      return undefined
    return WORKER_OFFLINE_BRANCH_REASON
  })
  // The row element, for its right-click / long-press menu.
  const [rowEl, setRowEl] = createContextMenuAnchor()
  return (
    <>
      <div
        ref={setRowEl}
        class={shared.node}
        style={{ 'padding-left': '36px' }}
        onClick={() => sel.toggleCollapsed(collapseKey())}
        data-testid="tab-tree-branch-group"
      >
        <ChevronRight
          size={14}
          class={`${shared.chevron} ${!sel.isCollapsed(collapseKey()) ? shared.chevronExpanded : ''}`}
        />
        {/* The one call site that LABELS the glyph. This row is a plain `div`
            with no `tabindex`, so its tooltip opens under a pointer alone --
            leaving a screen-reader user with the branch name and no way to
            tell a row that deletes as a directory from one that does not.
            Every other site prints the noun in text beside the glyph. */}
        <WorkingTreeIcon
          isWorktree={props.branch().isWorktree}
          size="sm"
          class={css.groupIcon}
          label={workingTreeKindLabel(props.branch().isWorktree)}
        />
        <RowLabelWithStats
          label={props.branch().displayLabel}
          // The kind of checkout and its directory are nowhere else on this
          // row, so the tooltip states both -- and `showWhen="always"` opens it
          // on every hover, not only when the label clips.
          //
          // The BRANCH NAME here, not `displayLabel`. The visible label appends
          // `(worker, ~/path)` when the name collides inside its repo, and the
          // rows below carry both of those facts already -- so the label would
          // print them twice in a tooltip three lines tall. `worker` is set
          // only when the branch name collides ACROSS workers, which is the
          // same test the visible suffix uses.
          showWhen="always"
          tooltipContent={(
            <WorkingTreeRows
              isWorktree={props.branch().isWorktree}
              name={props.branch().branchName ?? NO_BRANCH_LABEL}
              directory={props.branch().gitToplevel}
              homeDir={props.branch().homeDir}
              flavor={props.branch().flavor}
              worker={props.branch().workerLabel}
              stats={branchStats()}
            />
          )}
          stats={branchStats()}
        />
        {/* Hide the whole menu on the synthetic "(no branch)" group:
            branchName=null means the row's git state specifies no branch at
            all -- a repository with no commits yet, or a tab LeapMux has
            not stamped yet -- so the branch actions have no target and the
            new-tab items have no checkout to open in. Keeping the menu
            hidden is clearer than letting the user click into an error.

            A detached HEAD is NOT this case. The worker reports the short
            HEAD SHA as the branch (`branchOrShortSHA`), so the row carries
            a real label and keeps its menu. Delete then fails in the
            worker, because the label identifies a commit. */}
        {/* gitToplevel guard, defensive. A branch group forms only when
            `repoKeyAndLabel` resolved an origin URL or a toplevel, and every
            store writer that sets an origin URL sets a toplevel in the same
            patch — so an empty `gitToplevel` should not reach this row, and a
            tab with neither lands in `ungrouped` instead. The guard stays
            because the cost of being wrong is high: the branch actions
            would send `path: ""` to the worker, SanitizePath rejects empty,
            and the dialog opens stuck on a permission-denied banner. The
            new-tab items resolve no working directory at all and fall back
            to their dialog. */}
        <Show when={!sel.archived() && props.branch().branchName !== null && props.branch().gitToplevel !== '' ? actions.branchActions : undefined}>
          {branchActions => (
            <div class={sidebarActions}>
              <BranchContextMenu
                contextMenuFor={rowEl}
                isWorktree={props.branch().isWorktree}
                workerId={props.branch().workerId}
                disabledReason={menuDisabledReason()}
                actions={bindBranchActions(
                  branchActions(),
                  // Lazy: the ref is built at click time, from the branch this
                  // row shows THEN. A row survives a tree rebuild that swaps
                  // its branch object, so binding the ref eagerly would act on
                  // whichever branch the row happened to mount with.
                  () => buildBranchRef(sel.workspaceId(), props.branch(), sel.liveTab),
                )}
              />
            </div>
          )}
        </Show>
      </div>

      <div class={`${shared.childrenWrapper} ${!sel.isCollapsed(collapseKey()) ? shared.childrenWrapperExpanded : ''}`}>
        <div class={shared.childrenInner}>
          <TabLeafList tabs={props.branch().tabs} depth={3} />
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
  const groupStats = createMemo(() => diffStatsFromRepo(props.group()))
  const branchKeys = createStableKeys(() => props.group().branches, branchGroupKey)
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
          <KeyedFor each={branchKeys()} lookup={bKey => props.group().branchByKey.get(bKey)}>
            {(b, bKey) => (
              <BranchGroupRow
                branch={b}
                repoKey={props.repoKey}
                branchKey={bKey}
              />
            )}
          </KeyedFor>
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
  /**
   * The workspace this tree belongs to is archived, so its rows offer no
   * mutation: no branch menu, no close and no rename. A payload-backed tab
   * keeps its close -- see `canCloseTab`, which the tab bar shares.
   *
   * Named for the FACT rather than for the effect (this prop was `readOnly`).
   * Archival is the only thing that blocks mutation -- access is owner-only, and
   * `isWorkspaceMutatable` says so outright -- so the two were one concept
   * under two names, and a flag named for the effect invites a second caller to
   * set it for some other reason and re-open that gap.
   *
   * The branch menu is all-or-nothing here rather than dimmed with a reason:
   * every item of it either changes branch state or opens a tab, so an archived
   * workspace would leave a menu of nothing but dimmed items.
   */
  archived?: boolean
  workspaceId: string
  /**
   * Tile ids in their top-left-first traversal order of the workspace's
   * layout tree. Drives the per-branch sort: leaves appear in the same
   * order as their tiles do visually, ties broken by LexoRank `position`
   * (the tab bar's left-to-right order). Omit (or pass `[]`) and the
   * sort falls back to type → title.
   */
  tileOrder?: readonly string[]
  /**
   * Reactive lookup for worker display info. Used to disambiguate same-
   * named branches across distinct workers / clones (appending
   * `(worker-a)` or `(~/path)` to the branch label). When omitted, the
   * raw `workerId` and absolute toplevel path are used as fallbacks.
   */
  workerInfoFn?: (id: string) => WorkerInfo | null
  /**
   * Whether a Worker is currently reachable, read from the last state the Hub
   * pushed -- never probed here. The branch menu re-renders on every tree
   * recompute, so anything that touched the network would show up as menu lag.
   *
   * Answers `true` for a Worker it has no state for. Only a POSITIVE offline
   * reading disables the menu: an id missing from the Worker list means the
   * list has not loaded yet as often as it means the machine is gone, and
   * greying out a working action is worse than letting one fail.
   */
  isWorkerKnownOnline?: (workerId: string) => boolean
  /**
   * What every branch row's context menu can do, unbound. Each row binds the
   * bundle to its own {@link BranchRef}. Omit to render no branch menus.
   */
  branchActions?: BranchRefActions
  repoGitStore: ReturnType<typeof createRepoGitStore>
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
    () => props.tabs.map(t => tabBuildKey(t, props.repoGitStore)),
    [],
    { equals: shallowEqualArrays },
  )
  const tileOrderProjection = createMemo<readonly string[]>(
    () => props.tileOrder ?? [],
    [],
    { equals: shallowEqualArrays },
  )
  // workerInfoFn affects the cross-worker display label, the sort within a
  // branch, and the `homeDir`/`flavor`/`workerLabel` every branch row carries;
  // project by every worker id referenced in the tabs, mapped through the
  // lookup.
  //
  // THREE fields per worker, and `homeDir` is the one that decides the size of
  // this projection. It used to carry the name alone and to return a stable
  // empty array whenever the tabs referenced one worker or none: with a single
  // worker every `workerCount` collapses to ≤ 1, and the name is then the only
  // thing the label reads. Every row now reads `homeDir` to shorten its
  // directory, single worker included, and the worker's system info arrives on
  // its own RPC after the first paint. Keeping the shortcut froze the tree at
  // the build that ran before that answer landed, so each row's tooltip showed
  // an absolute path until some unrelated tab change happened to invalidate
  // the projection.
  //
  // The cost per distinct worker is one `infoMap` read, PLUS one synchronous
  // `localStorage.getItem` for a worker with no cached entry at all -- the
  // store memoizes a positive hit and not a miss, so an offline worker pays
  // that read on every recompute. The tabs of one workspace reference very few
  // workers, and the alternative (freezing the tree until some unrelated
  // change invalidates it) is the defect above.
  //
  // It carries the WHOLE record, and `workerProjectionsEqual` compares every
  // field: an enumerated read-list here would be a second source of truth, and
  // the next field `buildTree` reads without updating the list freezes the
  // tree again exactly as `homeDir` did.
  const workersProjection = createMemo<readonly WorkerProjectionEntry[]>(
    () => {
      const fn = props.workerInfoFn
      if (!fn)
        return []
      const ids = new Set<string>()
      for (const t of props.tabs) {
        if (t.workerId)
          ids.add(t.workerId)
      }
      return [...ids].sort().map(id => ({ id, info: fn(id) }))
    },
    [],
    { equals: workerProjectionsEqual },
  )
  // buildTree re-runs only when one of the three projections changes.
  // Each projection memo keeps its previous array reference when the
  // contents are unchanged (via shallowEqualArrays), so `on()` sees
  // stable identity on no-op pushes.
  const tree = createMemo(
    on(
      () => [tabsProjection(), tileOrderProjection(), workersProjection()] as const,
      () => buildTree(props.tabs, props.repoGitStore, props.tileOrder, props.workerInfoFn),
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
  const { keys: groupKeys, byKey: groupByKey } = createKeyedRows(() => tree().groups, g => g.repoKey)
  // The one live tab lookup every row resolves through. Built from `props.tabs`
  // rather than from `tree()` so it tracks the fields the tree's fingerprint
  // deliberately ignores -- see `RowSelectionContextValue.liveTab`.
  const tabByKey = createKeyLookup(() => props.tabs, tabKey)
  const storageKey = () => `${PREFIX_TAB_TREE}${props.workspaceId}`

  // --- Tab rename editing state ---
  const [editingTabKey, setEditingTabKey] = createSignal<string | null>(null)
  const [editingValue, setEditingValue] = createSignal('')
  let editCancelled = false
  const canClose = (tab: Tab) => canCloseTab(props.archived, tab)

  // `canRenameTab` states the rule; this surface adds only whether it HAS a
  // rename handler. Shared with the tab strip, which renders the same tabs.
  const canRename = (tab: Tab) =>
    canRenameTab(props.archived, tab) && Boolean(props.tabItemOps?.onRename)

  const startEditing = (tab: Tab) => {
    if (!canRename(tab))
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
    archived: () => props.archived,
    activeTabKey: () => props.activeTabKey,
    tabItemOps: () => props.tabItemOps,
    onTabClick: (type, id) => props.onTabClick(type, id),
    canClose,
    isCollapsed,
    toggleCollapsed,
    liveTab: key => tabByKey().get(key),
  }
  const editing: RowEditingContextValue = {
    editingTabKey,
    editingValue,
    setEditingValue,
    canRename,
    startEditing,
    commitEdit,
    cancelEdit,
  }
  const actions: BranchActionsContextValue = {
    get branchActions() {
      return props.branchActions
    },
    get isWorkerKnownOnline() {
      return props.isWorkerKnownOnline
    },
  }

  return (
    <RowSelectionContext.Provider value={selection}>
      <RowEditingContext.Provider value={editing}>
        <BranchActionsContext.Provider value={actions}>
          <div class={css.treeWrapper} data-testid="workspace-tab-tree">
            {/* Rows stay mounted across a WatchEvents push that reruns
                buildTree and re-emits the map: repoKey is a stable string. */}
            <KeyedFor each={groupKeys()} lookup={repoKey => groupByKey().get(repoKey)}>
              {(g, repoKey) => <RepoGroupRow group={g} repoKey={repoKey} />}
            </KeyedFor>

            {/* Ungrouped tabs (no git info) */}
            <TabLeafList tabs={tree().ungrouped} depth={1} />
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
  /**
   * The owning worker's home directory and path flavor, for the tilde
   * compression the row's tooltip applies to `gitToplevel`.
   *
   * Resolved in `buildTree` because only that function holds `workerInfoFn`;
   * the row itself has no route to a worker's home dir. They are the RAW
   * inputs, not a pre-shortened path: `WorkingTreeRows` owns the compression
   * rule for every surface, so the sidebar must not apply a second copy of it.
   * `homeDir` is empty and `flavor` is undefined until the worker's system
   * info arrives, which leaves the path absolute -- correct, and long.
   */
  homeDir: string
  flavor: PathFlavor | undefined
  /**
   * The worker's display name, but ONLY when this branch name appears on more
   * than one worker inside its repo. Empty otherwise.
   *
   * The same test the visible `displayLabel` suffix uses, so the row and its
   * tooltip agree on when the worker matters. Without it two workers holding
   * the same branch at the same path under each home directory produce two
   * rows whose tooltips are byte-identical, and Delete on one of them removes
   * the other machine's directory.
   */
  workerLabel: string
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
function repoKeyAndLabel(tab: Tab, store: RepoGitStore): { key: string, label: string } | null {
  const git = repoGitView(tab, store)
  if (git.originUrl)
    return { key: git.originUrl, label: formatGitOriginUrl(git.originUrl) }
  // Through the shared helper, so the group key and the branch bucket resolve
  // the toplevel the same way. A second copy of the chain here is what let a
  // stale row value override the worker's "not a git repository" answer. The
  // view resolved once at the top carries through.
  const toplevel = tabGitToplevelForKey(tab, store, git)
  if (toplevel) {
    const label = basename(toplevel) || toplevel
    return { key: repoKeyForLocal(toplevel), label }
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
export function sumDiffStatsFromTabs(tabs: Tab[], store: RepoGitStore): { added: number, deleted: number, untracked: number } {
  const seen = new Set<string>()
  let added = 0
  let deleted = 0
  let untracked = 0
  for (const t of tabs) {
    const git = repoGitView(t, store)
    if (!git.originUrl && !git.toplevel && !t.gitToplevel)
      continue
    const { added: a, deleted: d, untracked: u } = git.diffStats
    if (a === 0 && d === 0 && u === 0)
      continue
    // The view resolved once above and threads in: the branch key used to
    // resolve it two more times per tab.
    const key = tabBranchKey(t, store, git)
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
  store: RepoGitStore,
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
  // its git info: an origin URL, or a toplevel. Tabs with neither (non-git
  // dirs, and tabs not yet git-stamped) stay ungrouped.
  for (const tab of tabs) {
    const rk = repoKeyAndLabel(tab, store)
    if (!rk) {
      ungrouped.push(tab)
      continue
    }
    let entry = repoMap.get(rk.key)
    if (!entry) {
      entry = { label: rk.label, branches: new Map() }
      repoMap.set(rk.key, entry)
    }
    const git = repoGitView(tab, store)
    const branchName = git.branchLabel || null
    const workerId = tab.workerId ?? ''
    const gitToplevel = tabGitToplevelForKey(tab, store, git)
    // Through the shared function, not a second copy of its body. This IS the
    // "the sidebar groups its tree by it" caller tabBranchKey's own doc names,
    // and the composer's delete-branch dialog collects its tab list by the same
    // function -- a second membership test here would let the dialog report a
    // different set of affected tabs than the tree shows. The resolved view
    // passes in, so the loop's per-tab work is ONE resolution.
    const key = tabBranchKey(tab, store, git)
    let bucket = entry.branches.get(key)
    if (!bucket) {
      // Tabs are bucketed by (branchName, workerId, gitToplevel), so
      // every tab in a bucket shares the same worker view of the same
      // path — `isWorktree` is uniform across the bucket. Seed it
      // from the first tab; later tabs that happen to disagree (e.g.
      // a stale broadcast races a probe refresh) leave the seed as-is
      // rather than flickering the group's worktree flag.
      bucket = { branchName, workerId, gitToplevel, isWorktree: git.isWorktree ?? false, tabs: [] }
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
        const stats = repoGitView(t, store).diffStats
        // NOTE: one resolution per tab here is the deliberate remaining cost
        // -- the bucket's tabs share worker+toplevel but not necessarily the
        // same store key (subdir agents), and diffStats are per-entry.
        if (stats.added > 0 || stats.deleted > 0 || stats.untracked > 0) {
          diffAdded = stats.added
          diffDeleted = stats.deleted
          diffUntracked = stats.untracked
          break
        }
      }
      const stats = byBranchKey.get(branchNameSegment(branchName))
      const workerCount = stats?.workerIds.size ?? 1
      // ONE lookup for the label, the tooltip's tilde path and the worker row.
      // `computeBranchDisplayLabel` used to take the lookup function and call
      // it itself, so a colliding group resolved the same worker twice.
      const info = workerInfoFn?.(workerId)
      const displayLabel = computeBranchDisplayLabel(
        branchName,
        workerId,
        gitToplevel,
        workerCount,
        stats?.toplevels.size ?? 1,
        info,
      )
      return {
        branchName,
        workerId,
        gitToplevel,
        isWorktree,
        displayLabel,
        homeDir: info?.homeDir ?? '',
        flavor: workerPathFlavor(info),
        workerLabel: workerCount > 1 ? (info?.name || workerId) : '',
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
 * The path flavor to shorten a worker's paths with, or undefined when the
 * worker has not reported its OS.
 *
 * Undefined rather than a default, because `flavorFromOs(undefined)` answers
 * `'posix'` — which would force posix onto a Windows worker with no system info
 * yet, and stop `C:\Users\u\repo` from compressing at all. Undefined lets
 * `tildify` sniff the flavor from the path instead.
 */
function workerPathFlavor(info: WorkerInfo | null | undefined): PathFlavor | undefined {
  return info?.os ? flavorFromOs(info.os) : undefined
}

/**
 * Tilde-compress a worker-side absolute path for the collision suffix.
 *
 * One spelling of the rule for the whole tree: this suffix and the row's own
 * tooltip must shorten the same path the same way, or a row reads
 * `~/repos/foo` while its tooltip reads `/Users/x/repos/foo`. The tooltip gets
 * there by handing `WorkingTreeRows` the same `homeDir` and `flavor` this
 * function derives, so both end in one `tildify` call with equal arguments.
 * Returns the path unchanged while the worker's system info is absent — a
 * correct long path beats a wrong short one.
 */
function tildifyForWorker(path: string, info: WorkerInfo | null | undefined): string {
  return tildify(path, info?.homeDir, workerPathFlavor(info))
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
  info: WorkerInfo | null | undefined,
): string {
  const labelBase = branchName === null ? NO_BRANCH_LABEL : branchName
  if (workerCount <= 1 && toplevelCount <= 1)
    return labelBase
  const parts: string[] = []
  if (workerCount > 1) {
    const name = info?.name
    parts.push(name && name.length > 0 ? name : workerId)
  }
  if (toplevelCount > 1)
    parts.push(tildifyForWorker(gitToplevel, info))
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
