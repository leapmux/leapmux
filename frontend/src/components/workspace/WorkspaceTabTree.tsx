import type { Accessor, Component } from 'solid-js'
import type { BranchRefActions } from './branchActions'
import type { RepoCheckout } from './repoCheckouts'
import type { BranchGroup, BranchRef, RepoGroup, TabNode, WorkerProjectionEntry } from './workspaceTabTree.model'
import type { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { Tab, TabItemOps } from '~/stores/tab.types'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FolderGit from 'lucide-solid/icons/folder-git'
import X from 'lucide-solid/icons/x'
import { createMemo, createSignal, on, Show, useContext } from 'solid-js'
import { DragHandle } from '~/components/common/DragHandle'
import { createContextMenuAnchor } from '~/components/common/DropdownMenu'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { NotificationDot } from '~/components/common/NotificationDot'
import { TabContextMenu } from '~/components/common/TabContextMenu'
import { TabTypeIcon } from '~/components/common/TabTypeIcon'
import { Tooltip } from '~/components/common/Tooltip'
import { WorkingTreeIcon, workingTreeKindLabel, WorkingTreeRows } from '~/components/common/WorkingTree'
import { SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { PREFIX_TAB_TREE, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { createStableContext } from '~/lib/createStableContext'
import { attachDragActivators } from '~/lib/dragActivators'
import { createGuardedDraggableRow } from '~/lib/dragRow'
import { createKeyedRows, createKeyLookup, createStableKeys, KeyedFor } from '~/lib/keyedRows'
import { shallowEqualArrays } from '~/lib/shallowEqual'
import { diffStatsFromRepo } from '~/stores/repoGit'
import { canCloseTab, canRenameTab, tabDisplayLabel, tabKey, tabTooltipShowWhen, tabTooltipText, terminalProgressBarProps, terminalProgressVisible } from '~/stores/tab.helpers'
import { isTerminalTab } from '~/stores/tab.types'
import * as tabBarStyles from '../shell/TabBar.css'
import { terminalStatusClassList } from '../shell/terminalStatus'
import { RowLabelWithStats } from '../tree/gitStatusUtils'
import * as shared from '../tree/sharedTree.css'
import { menuTrigger, sidebarActions } from '../tree/sidebarActions.css'
import { bindBranchActions, WORKER_OFFLINE_BRANCH_REASON, WORKER_OFFLINE_NEW_TAB_REASON } from './branchActions'
import { BranchContextMenu } from './BranchContextMenu'
import {
  collapseKeyForBranch,
  repoKeyTooltip,
  repoOriginUrlFromKey,
} from './branchKeys'
import { listRepoCheckouts } from './repoCheckouts'
import { RepoContextMenu } from './RepoContextMenu'
import * as css from './workspaceTabTree.css'
import {
  anyTabHasNotification,
  branchGroupKey,
  buildBranchRef,
  buildTree,
  nestSubagentTabs,
  NO_BRANCH_LABEL,
  tabBuildKey,
  workerProjectionsEqual,
} from './workspaceTabTree.model'

// --- Tab leaf node ---

/**
 * The dot a FOLDED row shows for everything under it.
 *
 * Three surfaces fold -- a branch row, a repository row, and the workspace row
 * one module up -- and each pairs its own collapse flag with its own tab list.
 * Everything after that pairing is the same rule, so it lives here once: an
 * EXPANDED row shows nothing, because every leaf under it shows its own dot and
 * a second one on the header would repeat it.
 *
 * `folded()` is read first, so an expanded row never reads `tabs()` and never
 * subscribes to the per-tab signals behind it.
 */
export const RolledUpNotificationDot: Component<{
  folded: () => boolean
  tabs: () => readonly Tab[]
}> = (props) => {
  const marked = createMemo(() => props.folded() && anyTabHasNotification(props.tabs()))
  return (
    <Show when={marked()}>
      <NotificationDot testId="sidebar-tab-notification" />
    </Show>
  )
}

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
    'y',
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
      <Show when={props.tab.hasNotification}>
        <NotificationDot testId="sidebar-tab-notification" />
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
            size="md"
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
  canClose: () => boolean
  isCollapsed: (key: string) => boolean
  toggleCollapsed: (key: string) => void
  /**
   * Set many rows at once, in ONE signal write. "Collapse all branches" would
   * otherwise re-render the tree once per branch of the repository.
   */
  setCollapsedMany: (keys: readonly string[], collapsed: boolean) => void
  /** Whether a Worker is THIS machine. See `~/lib/workerLocality`. */
  isLocalWorker: (workerId: string) => boolean
  /**
   * The tab a key identifies RIGHT NOW, straight off `props.tabs` -- never off the
   * cached tree. Reactive: reading it inside a row subscribes that row to its
   * own tab, so a metadata-only change (a rename, a hydrated title/provider, a
   * terminal status flip) updates the row in place without rebuilding the tree.
   * Returns undefined for a tab closed since the last rebuild.
   */
  liveTab: (key: string) => Tab | undefined
  /**
   * The live objects for these CACHED tabs, with every key that no longer
   * resolves dropped -- a tab closed since the last rebuild.
   *
   * A caller that holds a branch's or a repository's `tabs` reads them through
   * this and never straight off the cached tree. The tree's fingerprint (see
   * `tabBuildKey`) deliberately excludes the fields that change at PTY-read
   * frequency, so a cached object holds whatever those fields were at the last
   * rebuild. Three callers need it -- both notification roll-ups and the
   * branch-dialog snapshot -- which is why the rule lives here rather than in
   * each of them.
   */
  liveTabs: (tabs: readonly Tab[]) => Tab[]
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
      canClose={sel.canClose()}
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
        <RolledUpNotificationDot
          folded={() => sel.isCollapsed(collapseKey())}
          tabs={() => sel.liveTabs(props.branch().tabs)}
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
                repository={() => ({
                  gitToplevel: props.branch().gitToplevel,
                  originUrl: repoOriginUrlFromKey(props.repoKey),
                  isLocal: sel.isLocalWorker(props.branch().workerId),
                })}
                disabledReason={menuDisabledReason()}
                actions={bindBranchActions(
                  branchActions(),
                  // Lazy: the ref is built at click time, from the branch this
                  // row shows THEN. A row survives a tree rebuild that swaps
                  // its branch object, so binding the ref eagerly would act on
                  // whichever branch the row happened to mount with.
                  () => buildBranchRef(sel.workspaceId(), props.branch(), sel.liveTabs),
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
  const actions = useBranchActions()
  const groupStats = createMemo(() => diffStatsFromRepo(props.group()))
  const branchKeys = createStableKeys(() => props.group().branches, branchGroupKey)
  // The row element, for its right-click / long-press menu.
  const [rowEl, setRowEl] = createContextMenuAnchor()

  // Built only while the menu is open: one of these mounts per repository row
  // of every workspace, and the projection walks every branch under it.
  const [menuOpen, setMenuOpen] = createSignal(false)
  const checkouts = createMemo((): RepoCheckout[] => {
    if (!menuOpen())
      return []
    return listRepoCheckouts(
      props.group().branches,
      repoOriginUrlFromKey(props.repoKey),
      sel.isLocalWorker,
    )
  })

  const collapseKeys = () =>
    props.group().branches.map(b => collapseKeyForBranch(props.repoKey, branchGroupKey(b)))

  return (
    <>
      <div
        ref={setRowEl}
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
        {/* Every tab under the repository, not only the branch rows: folding
            this row hides those rows along with their own roll-ups. */}
        <RolledUpNotificationDot
          folded={() => sel.isCollapsed(props.repoKey)}
          tabs={() => sel.liveTabs(props.group().branches.flatMap(b => b.tabs))}
        />
        {/* Hidden for an ARCHIVED workspace, like the branch row's menu: its
            tab-creation items are mutations, and the read-only remainder is
            reachable from the workspace row, which keeps its own copy. */}
        <Show when={!sel.archived()}>
          <div class={sidebarActions}>
            <RepoContextMenu
              contextMenuFor={rowEl}
              checkouts={checkouts}
              actionsFor={(checkout) => {
                const bundle = actions.branchActions
                if (!bundle || checkout.branch.branchName === null)
                  return undefined
                // Lazy, like the branch row's: the ref is built at click time
                // from the branch this row shows THEN, because a row survives
                // a tree rebuild that swaps its branch object.
                return bindBranchActions(
                  bundle,
                  () => buildBranchRef(sel.workspaceId(), checkout.branch, sel.liveTabs),
                )
              }}
              disabledReasonFor={(checkout) => {
                const isOnline = actions.isWorkerKnownOnline
                // The items this disables are New agent and New terminal, so
                // the sentence must name those, not the branch actions.
                return !isOnline || isOnline(checkout.workerId)
                  ? undefined
                  : WORKER_OFFLINE_NEW_TAB_REASON
              }}
              onToggle={setMenuOpen}
              onCollapseAllBranches={() => sel.setCollapsedMany(collapseKeys(), true)}
              nothingToCollapse={() => collapseKeys().every(k => sel.isCollapsed(k))}
            />
          </div>
        </Show>
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
   * mutation: no branch menu, no close, and no rename. See `canCloseTab`,
   * which the tab bar shares.
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
  /**
   * Whether a Worker runs on THIS machine, so the local file manager and the
   * local applications can open a path it reports. See `~/lib/workerLocality`.
   *
   * REQUIRED. It was optional with a `?? false` default, and the default is
   * exactly the failure: a surface that forgot the hand-off rendered a menu
   * missing two items, with no type error and no failing test. Every render
   * site states the answer now, including the tests that do not care -- which
   * is the point, because the three that DO care are then the only ones that
   * say anything but `() => false`.
   */
  isLocalWorkerFn: (workerId: string) => boolean
  repoGitStore: ReturnType<typeof createRepoGitStore>
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
  // The cost per distinct worker is one `infoMap` read, plus -- for a worker
  // with no cached entry at all -- a subscription to that worker's hydration.
  // The stored read behind it is asynchronous and the store issues at most one
  // at a time per worker, so an offline worker costs a recompute when its row
  // lands and nothing on the recomputes after that. The tabs of one workspace
  // reference very few workers, and the alternative (freezing the tree until
  // some unrelated change invalidates it) is the defect above.
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
  const canClose = () => canCloseTab(props.archived)

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

  /**
   * Change the collapse map and persist it, in ONE signal write.
   *
   * The only writer, so the sessionStorage write has one home and a later
   * change to the persistence cannot reach one path and miss the other. Solid
   * calls an updater exactly once and synchronously, before it writes the
   * signal, so persisting from inside it is safe.
   */
  function writeCollapsed(mutate: (draft: Record<string, boolean>) => void) {
    setCollapsed((prev) => {
      const next = { ...prev }
      mutate(next)
      sessionStorageSet(storageKey(), next)
      return next
    })
  }

  function toggleCollapsed(key: string) {
    writeCollapsed((draft) => {
      draft[key] = !draft[key]
    })
  }

  function setCollapsedMany(keys: readonly string[], value: boolean) {
    // Nothing to write when every key already holds `value`. The menu item that
    // calls this is disabled in exactly that case, so this guards a caller that
    // does not exist yet -- but returning `prev` notifies nobody, where a fresh
    // object re-renders the whole tree and rewrites storage for no change.
    if (keys.every(key => (collapsed()[key] ?? false) === value))
      return
    writeCollapsed((draft) => {
      for (const key of keys)
        draft[key] = value
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
    setCollapsedMany,
    isLocalWorker: workerId => props.isLocalWorkerFn(workerId),
    liveTab: key => tabByKey().get(key),
    liveTabs: tabs => tabs
      .map(t => tabByKey().get(tabKey(t)))
      .filter((t): t is Tab => t !== undefined),
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
