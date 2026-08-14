import type { Accessor, Component } from 'solid-js'
import type { FileSortFields, FileSortKey, FileSortOrder } from '~/lib/fileSort'
import type { PathFlavor } from '~/lib/paths'
import type { createGitFileStatusStore, DiffStats } from '~/stores/gitFileStatus.store'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import File from 'lucide-solid/icons/file'
import FolderClosed from 'lucide-solid/icons/folder-closed'
import FolderOpen from 'lucide-solid/icons/folder-open'
import { batch, createEffect, createMemo, createSignal, For, Match, on, onCleanup, onMount, Show, Switch, useContext } from 'solid-js'
import { createStore, produce, reconcile } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { createContextMenuAnchor } from '~/components/common/DropdownMenu'
import { FileActionsMenu } from '~/components/common/FileActionsMenu'
import { Icon } from '~/components/common/Icon'
import { StartupSpinner } from '~/components/common/StartupPanel'
import { PREFIX_DIRECTORY_TREE, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { createStableContext } from '~/lib/createStableContext'
import { formatErrorMessage } from '~/lib/errors'
import { DEFAULT_FILE_SORT_ORDER, makeFileComparator } from '~/lib/fileSort'
import { basename, detectFlavor, relativeUnder } from '~/lib/paths'
import { prefersReducedMotion } from '~/lib/prefersReducedMotion'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { emptyState } from '~/styles/shared.css'
import * as styles from './DirectoryTree.css'
import { getGitFileIconClass, RowLabelWithStats } from './gitStatusUtils'
import { menuTrigger, sidebarActions } from './sidebarActions.css'

export interface DirectoryTreeHandle {
  collapseAll: () => void
  refresh: () => void
}

export interface DirectoryTreeProps {
  workerId: string
  showFiles?: boolean
  selectedPath: string
  onSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  rootPath?: string
  homeDir?: string
  /**
   * Path flavor for the worker this tree is rendering. Defaults to a
   *  best-effort sniff from homeDir/rootPath.
   */
  flavor?: PathFlavor
  gitStatusStore?: ReturnType<typeof createGitFileStatusStore>
  /**
   * When set, the tree is FILTERED: only nodes this predicate accepts render.
   * Built by the git-aware caller (see makeGitVisibilityPredicate), so the
   * untracked-subtree semantics stay out of the generic tree. Its presence is
   * also what "is this tree filtered?" means.
   */
  isVisible?: (path: string) => boolean
  /** Signal bumped on agent turn-end; drives directory tree refresh. */
  turnEndTrigger?: number
  /** When false, entries with hidden=true are filtered out. Defaults to true. */
  showHiddenFiles?: boolean
  /**
   * Display order for the rows within each directory. Defaults to
   * {@link DEFAULT_FILE_SORT_ORDER}. Applied at render time, so a change
   * reorders the cached listing without re-fetching it.
   */
  sortOrder?: FileSortOrder
  /**
   * When false, the initial root-children fetch is suppressed. Used to
   * defer a directory listing for a tab whose working dir isn't on disk
   * yet (e.g. a worktree-creating agent during its STARTING window —
   * fetching now would cache a partial listing that persists until the
   * user manually refreshes). Flipping back to true triggers the load
   * effect to run, which then fetches normally.
   */
  enabled?: boolean
  /** Ref callback for imperative actions (collapse all, etc.). */
  ref?: (handle: DirectoryTreeHandle) => void
}

interface TreeNodeData {
  path: string
  displayName: string
  isDir: boolean
  hidden: boolean
  /**
   * Bytes, from the listing's stat. For a DIRECTORY this is the inode's own
   * size, which says nothing about its contents — nothing displays or sorts on
   * it (see `makeFileComparator` and `FileActionsMenu`'s `showSize`).
   */
  size: number
  /** RFC3339 UTC, from the listing's stat. */
  modTime: string
}

/** The fields the sort comparator reads off a tree node. */
const TREE_SORT_FIELDS: FileSortFields<TreeNodeData> = {
  name: node => node.displayName,
  isDir: node => node.isDir,
  size: node => node.size,
  modTime: node => node.modTime || undefined,
}

// Content equality for the children cache; see setChildrenInStore.
function sameTreeEntries(a: readonly TreeNodeData[], b: readonly TreeNodeData[]): boolean {
  if (a === b)
    return true
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]
    const y = b[i]
    // size and modTime are part of the comparison because the tree SORTS and
    // DISPLAYS them: without them, a file whose contents changed but whose
    // name did not would keep its stale size in the three-dot menu and its
    // stale position under a size or modified sort. It does cost fast-path
    // hits that the pre-sort tree never lost -- see setChildrenInStore.
    if (x.path !== y.path || x.displayName !== y.displayName || x.isDir !== y.isDir
      || x.hidden !== y.hidden || x.size !== y.size || x.modTime !== y.modTime) {
      return false
    }
  }
  return true
}

// -------------------------------------------------------------------------
// Tree context — bundles stable, tree-wide values to avoid prop drilling
// -------------------------------------------------------------------------

interface TreeContextValue {
  workerId: string
  showFiles: boolean
  rootPath: string
  homeDir?: string
  flavor: () => PathFlavor
  scrollContainer?: HTMLDivElement
  gitStatusStore: () => ReturnType<typeof createGitFileStatusStore> | undefined
  showHiddenFiles: boolean
  /** Comparator for the current sort order, shared by every node. */
  comparator: () => (a: TreeNodeData, b: TreeNodeData) => number
  /** The inline notice for a directory the worker truncated, bound to the current sort. */
  truncationNotice: (path: string, shown: number) => string
  isVisible: () => ((path: string) => boolean) | undefined
  refreshVersion: () => number
  onSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  isNodeExpanded: (path: string) => boolean
  setNodeExpanded: (path: string, expanded: boolean) => void
  getChildren: (path: string) => TreeNodeData[] | undefined
  setChildren: (path: string, data: TreeNodeData[], truncated: boolean, totalEntries: number) => void
  isTruncated: (path: string) => boolean
}

const TreeContext = createStableContext<TreeContextValue>('tree/DirectoryTree')

function useTree(): TreeContextValue {
  const ctx = useContext(TreeContext)
  if (!ctx)
    throw new Error('useTree must be used within a TreeContext.Provider')
  return ctx
}

// -------------------------------------------------------------------------
// Serialization helpers for sessionStorage
// -------------------------------------------------------------------------

/**
 * Schema version for the persisted tree state.
 *
 * Bump this whenever the SHAPE of anything in the payload changes -- a field
 * added to or removed from `TreeNodeData`, or a change to how the two path maps
 * are keyed. The next load then discards the whole payload and re-fetches,
 * instead of hydrating a shape the current code misreads.
 *
 * A version, not a per-field probe: the probe this replaced tested two of
 * `TreeNodeData`'s six fields and could not reach `expandedPaths` or
 * `truncatedDirs` at all, so the next field added here would have needed
 * someone to remember to extend it. Same mechanism as
 * `STORED_ROW_HEIGHTS_VERSION` in the chat's row-height cache.
 *
 * 1: entries carry `size` and `modTime`, so the sidebar can sort by them.
 */
export const DIRECTORY_TREE_STATE_VERSION = 1

interface DirectoryTreeStateJSON {
  v?: number
  expandedPaths: Record<string, boolean>
  childrenCache: Record<string, TreeNodeData[]>
  truncatedDirs?: Record<string, number>
}

function serializeState(
  expandedPaths: Record<string, boolean>,
  childrenCache: Record<string, TreeNodeData[]>,
  truncatedDirs: Record<string, number>,
): string {
  return JSON.stringify({ v: DIRECTORY_TREE_STATE_VERSION, expandedPaths, childrenCache, truncatedDirs })
}

/**
 * Restores the persisted tree state, or null when the payload is unusable.
 *
 * A version mismatch discards EVERYTHING, expansion included. That costs a
 * collapsed tree once per bump, and it buys the one rule that covers every key
 * in the payload: a partial restore would have to prove, for each surviving
 * key, that the old shape still reads correctly under the new code.
 */
function deserializeState(raw: string): { expandedPaths: Record<string, boolean>, childrenCache: Record<string, TreeNodeData[]>, truncatedDirs: Record<string, number> } | null {
  try {
    const json: DirectoryTreeStateJSON = JSON.parse(raw)
    if (!json || typeof json !== 'object' || json.v !== DIRECTORY_TREE_STATE_VERSION)
      return null
    return {
      expandedPaths: json.expandedPaths ?? {},
      // Still filtered, for a payload that is the right VERSION but corrupt --
      // a hand edit, or a truncated write. The version answers "is this shape
      // current"; this answers "is this value well formed".
      childrenCache: wellFormedCachedChildren(json.childrenCache),
      truncatedDirs: json.truncatedDirs ?? {},
    }
  }
  catch {
    return null
  }
}

/** Drops any cached directory whose entries are not a well-formed array. */
function wellFormedCachedChildren(cache: Record<string, TreeNodeData[]> | undefined): Record<string, TreeNodeData[]> {
  const usable: Record<string, TreeNodeData[]> = {}
  for (const [path, entries] of Object.entries(cache ?? {})) {
    if (!Array.isArray(entries))
      continue
    if (entries.every(e => typeof e?.path === 'string' && typeof e?.displayName === 'string'))
      usable[path] = entries
  }
  return usable
}

// -------------------------------------------------------------------------
// Visibility helpers
// -------------------------------------------------------------------------

function isDescendantPath(child: string, parent: string, flavor: PathFlavor): boolean {
  const rel = relativeUnder(child, parent, flavor)
  return rel !== null && rel !== ''
}

/**
 * The inline row shown under a directory the worker truncated.
 *
 * The worker sorts by name and cuts at its entry limit BEFORE stat-ing, so a
 * sort by anything else orders only the entries that survived that cut. The
 * notice names the window, so the user does not read a partial answer as a
 * complete one.
 */
function formatTruncationNotice(shown: number, total: number, sortKey: FileSortKey): string {
  // The worker reports what the directory really held, so the notice names the
  // size of what is hidden instead of only that something is. `total` is 0 for
  // a listing restored from a cache written before the worker sent it.
  const count = total > shown ? `${shown} of ${total} entries` : `${shown}+ entries`
  return sortKey === 'name'
    ? `${count}, listing truncated`
    : `${count}, truncated by name before sorting`
}

/** The rows one directory renders: hidden/git filters first, then the sort. */
function visibleSortedChildren(
  all: readonly TreeNodeData[],
  showHidden: boolean,
  isVisible: ((path: string) => boolean) | undefined,
  comparator: (a: TreeNodeData, b: TreeNodeData) => number,
): TreeNodeData[] {
  const filtered = showHidden && !isVisible
    ? all
    : all.filter(c => (showHidden || !c.hidden) && (!isVisible || isVisible(c.path)))
  return filtered.toSorted(comparator)
}

// -------------------------------------------------------------------------
// File listing
// -------------------------------------------------------------------------

// No sort here: the cache holds the worker's order (directories first, then
// name), and the display order is applied when the rows render, so changing the
// sort reorders what is already on screen instead of re-fetching every
// expanded directory.
async function loadChildren(
  workerId: string,
  dirPath: string,
  showFiles: boolean,
): Promise<{ entries: TreeNodeData[], truncated: boolean, totalEntries: number }> {
  const resp = await workerRpc.listDirectory(workerId, { workerId, path: dirPath, maxDepth: 5, dirsOnly: !showFiles })

  return {
    entries: resp.entries.map(entry => ({
      path: entry.path,
      displayName: entry.name,
      isDir: entry.isDir,
      hidden: entry.hidden,
      // `size` is a protobuf int64, so the wire type is bigint. File sizes stay
      // far below Number.MAX_SAFE_INTEGER, and JSON.stringify -- which the
      // sessionStorage cache runs on this value -- throws on a bigint.
      size: Number(entry.size ?? 0n),
      modTime: entry.modTime,
    })),
    truncated: resp.truncated,
    // `?? 0` for a worker that predates the field: the notice then falls back
    // to "N+ entries" rather than claiming a total of zero.
    totalEntries: resp.totalEntries ?? 0,
  }
}

/**
 * Three-dot context menu for a tree node (file or directory).
 *
 * `size` and `modTime` come from the cached listing rather than a fresh
 * StatFile call, so the menu opens with no round trip and shows exactly the
 * values the current sort ordered the row by. The root row is the one
 * exception — it has no parent listing here, so it stats itself once.
 */
const TreeContextMenu: Component<{
  path: string
  isDir: boolean
  size?: number
  modTime?: string
  contextMenuFor?: Accessor<HTMLElement | undefined>
}> = (props) => {
  const tree = useTree()
  return (
    <FileActionsMenu
      contextMenuFor={props.contextMenuFor}
      workerId={tree.workerId}
      path={props.path}
      flavor={tree.flavor()}
      isDir={props.isDir}
      rootPath={tree.rootPath}
      homeDir={tree.homeDir}
      size={props.size}
      modTime={props.modTime}
      onMention={tree.onMention}
      onOpenTerminal={tree.onOpenTerminal}
      triggerClass={menuTrigger}
      triggerTestId="tree-context-button"
      itemTestIdPrefix="tree"
    />
  )
}

interface GitIconInfo { class: string, testId: string | undefined }
const NO_GIT_ICON: GitIconInfo = { class: '', testId: undefined }

const TreeNode: Component<{
  node: TreeNodeData
  selectedPath: string
  depth: number
}> = (props) => {
  const tree = useTree()
  const [loading, setLoading] = createSignal(false)
  let wrapperRef!: HTMLDivElement
  // `nodeRef` stays for the imperative scroll-into-view callers.
  let nodeRef!: HTMLDivElement
  // The same element as `nodeRef`, for the row menu's attach effect.
  const [nodeEl, setNodeEl] = createContextMenuAnchor()
  let childrenRef: HTMLDivElement | undefined

  const expanded = () => tree.isNodeExpanded(props.node.path)
  const isSelected = () => props.selectedPath === props.node.path
  const allChildren = () => tree.getChildren(props.node.path) ?? []
  // A memo, not a plain closure: `<For>`, the truncation notice, the empty
  // state and the auto-expand effect all read it, and each read would
  // otherwise re-filter and re-sort the whole directory.
  //
  // `toSorted`, never `sort`: `all` is the live store array, and mutating it
  // outside setState desyncs the store from what `sameTreeEntries` compares
  // against. The sorted copy holds the SAME store objects, so `<For>` moves
  // the existing rows instead of disposing and rebuilding them.
  //
  // ACCEPTED CONSEQUENCE: under a `modified` or `size` order, a turn-end
  // refresh that changes one file's stat can move rows. Moving a connected node
  // runs its removing steps, and the removing steps of a showing popover hide
  // it — so a three-dot menu open on a row that moves closes under the pointer.
  // The default `name` order cannot trigger this, because a rename is already a
  // structural change.
  const children = createMemo(() => visibleSortedChildren(
    allChildren(),
    tree.showHiddenFiles,
    tree.isVisible(),
    tree.comparator(),
  ))
  const loaded = () => tree.getChildren(props.node.path) !== undefined

  const doScroll = () => {
    const container = tree.scrollContainer
    if (!container || !wrapperRef)
      return
    const containerRect = container.getBoundingClientRect()
    const wrapperRect = wrapperRef.getBoundingClientRect()
    if (wrapperRect.bottom > containerRect.bottom) {
      // Scroll so the children are visible, but clamp so the node
      // row itself (the selected directory) stays visible at the top.
      const nodeRowHeight = nodeRef ? nodeRef.getBoundingClientRect().height : 0
      const overflow = wrapperRect.bottom - containerRect.bottom
      const maxScroll = wrapperRect.top - containerRect.top - nodeRowHeight
      container.scrollTop += Math.min(overflow, Math.max(0, maxScroll))
    }
  }

  const scrollIntoViewIfNeeded = () => {
    if (!childrenRef) {
      requestAnimationFrame(doScroll)
      return
    }
    // Wait for the CSS grid-template-rows expand transition to finish
    // so that wrapperRef has its full height when we measure.
    // When prefers-reduced-motion is enabled, transitions are instant
    // so transitionend never fires — use requestAnimationFrame instead.
    if (prefersReducedMotion()) {
      requestAnimationFrame(doScroll)
      return
    }
    const onEnd = (e: TransitionEvent) => {
      if (e.target !== childrenRef)
        return
      childrenRef!.removeEventListener('transitionend', onEnd)
      doScroll()
    }
    childrenRef.addEventListener('transitionend', onEnd)
  }

  const doLoad = async () => {
    if (loaded() || loading())
      return
    setLoading(true)
    try {
      const result = await loadChildren(tree.workerId, props.node.path, tree.showFiles)
      tree.setChildren(props.node.path, result.entries, result.truncated, result.totalEntries)
    }
    catch {
      // ignore load errors
    }
    finally {
      setLoading(false)
    }
  }

  const toggle = async () => {
    if (!props.node.isDir) {
      tree.onSelect(props.node.path)
      tree.onFileOpen?.(props.node.path)
      return
    }
    await doLoad()
    const willExpand = !expanded()

    // Set expanded state before onSelect so that the scroll-on-select
    // effect sees the correct state and skips scrolling on collapse.
    tree.setNodeExpanded(props.node.path, willExpand)
    tree.onSelect(props.node.path)
    if (willExpand) {
      scrollIntoViewIfNeeded()
    }
  }

  // Auto-expand when selectedPath changes to a descendant of this node.
  createEffect(on(
    () => props.selectedPath,
    (selected) => {
      if (!props.node.isDir)
        return
      const flavor = tree.flavor()
      if (!isDescendantPath(selected, props.node.path, flavor))
        return

      if (!loaded()) {
        doLoad().then(() => { // eslint-disable-line solid/reactivity -- one-shot async load
          tree.setNodeExpanded(props.node.path, true)
          // Scroll into view for the deepest auto-expanded node.
          // Only scroll if this is the closest ancestor (children will handle deeper).
          const hasMatchingChild = children().some(
            c => c.isDir && (isDescendantPath(selected, c.path, flavor) || selected === c.path),
          )
          if (!hasMatchingChild) {
            scrollIntoViewIfNeeded()
          }
        })
      }
      else if (!expanded()) {
        tree.setNodeExpanded(props.node.path, true)
      }
    },
  ))

  // Re-fetch when expanded but cache is missing (e.g. after sessionStorage restore).
  createEffect(() => {
    if (props.node.isDir && expanded() && !loaded() && !loading()) {
      doLoad()
    }
  })

  // Silently re-fetch when refreshVersion bumps (keeps old data visible).
  createEffect(on(
    () => tree.refreshVersion(),
    (_, prev) => {
      if (prev === undefined)
        return
      if (!props.node.isDir || !expanded())
        return
      loadChildren(tree.workerId, props.node.path, tree.showFiles)
        .then((result) => { // eslint-disable-line solid/reactivity -- one-shot async refresh
          tree.setChildren(props.node.path, result.entries, result.truncated, result.totalEntries)
        })
        .catch(() => { /* ignore refresh errors */ })
    },
  ))

  // Scroll into view when this node becomes the selected one without the user
  // clicking it: Locate active file, a selection restored on tab switch, or a
  // path typed into the dialog picker's PathInput, whose onSubmit feeds the
  // same selectedPath.
  // Skip for directories that are collapsed — collapsing should not scroll.
  createEffect(() => {
    if (props.selectedPath === props.node.path && nodeRef) {
      if (props.node.isDir && !expanded())
        return
      const container = tree.scrollContainer
      if (!container)
        return
      requestAnimationFrame(() => {
        const containerRect = container.getBoundingClientRect()
        const nodeRect = nodeRef.getBoundingClientRect()
        if (nodeRect.top < containerRect.top || nodeRect.bottom > containerRect.bottom) {
          container.scrollTop += nodeRect.top - containerRect.top
        }
      })
    }
  })

  const indent = () => `${8 + props.depth * 16}px`
  const gitIcon = createMemo<GitIconInfo>(() => {
    const store = tree.gitStatusStore()
    if (!store)
      return NO_GIT_ICON
    if (props.node.isDir) {
      return store.hasChanges(props.node.path)
        ? { class: styles.iconDirChanged, testId: undefined }
        : NO_GIT_ICON
    }
    const entry = store.getFileStatus(props.node.path)
    return entry ? getGitFileIconClass(entry) : NO_GIT_ICON
  })
  const diffStats = createMemo<DiffStats | null>(() => {
    const store = tree.gitStatusStore()
    return store ? store.getNodeDiffStats(props.node.path, props.node.isDir) : null
  })

  return (
    <div ref={wrapperRef}>
      <div
        ref={(el) => {
          nodeRef = el
          setNodeEl(el)
        }}
        class={styles.node}
        classList={{ [styles.nodeSelected]: isSelected() }}
        // The row's own statement of selection, so the coarse-pointer kebab
        // reveal (~/components/tree/sidebarActions.css.ts) keys on ONE marker
        // for every row type, not on each type's style class.
        data-active={isSelected() ? 'true' : 'false'}
        style={{ 'padding-left': indent() }}
        data-testid="tree-row"
        onClick={toggle}
      >
        <Show
          when={props.node.isDir}
          fallback={<span class={styles.chevronPlaceholder} />}
        >
          <Icon icon={ChevronRight} size="md" class={`${styles.chevron}${expanded() ? ` ${styles.chevronExpanded}` : ''}`} />
        </Show>
        <Show
          when={props.node.isDir}
          fallback={<Icon icon={File} size="sm" class={gitIcon().class || styles.fileIcon} data-testid={gitIcon().testId} />}
        >
          <Show
            when={expanded()}
            fallback={<Icon icon={FolderClosed} size="sm" class={gitIcon().class || styles.folderIcon} data-testid={gitIcon().testId} />}
          >
            <Icon icon={FolderOpen} size="sm" class={gitIcon().class || styles.folderIcon} data-testid={gitIcon().testId} />
          </Show>
        </Show>
        <RowLabelWithStats
          // The row's own text is not the name: the three-dot menu renders
          // inside the row and stays mounted while closed, so its items are
          // part of the row's textContent. Anything matching on the name needs
          // this hook.
          label={<span data-testid="tree-row-name" class={props.node.hidden ? styles.nodeNameMuted : styles.nodeName}>{props.node.displayName}</span>}
          tooltipLabel={props.node.displayName}
          stats={diffStats()}
        />
        <div class={sidebarActions}>
          <TreeContextMenu
            contextMenuFor={nodeEl}
            path={props.node.path}
            isDir={props.node.isDir}
            size={props.node.size}
            modTime={props.node.modTime}
          />
        </div>
      </div>
      <Show when={loading()}>
        <div class={styles.loadingInline} style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}>
          Loading...
        </div>
      </Show>
      <Show when={loaded()}>
        <div ref={childrenRef} class={styles.childrenWrapper} classList={{ [styles.childrenWrapperExpanded]: expanded() && !loading() }}>
          <div class={styles.childrenInner}>
            <For each={children()}>
              {child => (
                <TreeNode
                  node={child}
                  selectedPath={props.selectedPath}
                  depth={props.depth + 1}
                />
              )}
            </For>
            <Show when={children().length === 0}>
              <div class={styles.emptyInline} style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}>
                Empty
              </div>
            </Show>
            <Show when={tree.isTruncated(props.node.path) && !tree.isVisible()}>
              <div class={styles.emptyInline} style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}>
                {tree.truncationNotice(props.node.path, children().length)}
              </div>
            </Show>
          </div>
        </div>
      </Show>
    </div>
  )
}

export const DirectoryTree: Component<DirectoryTreeProps> = (props) => {
  const [loading, setLoading] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  let loadVersion = 0
  let treeRef!: HTMLDivElement
  // The root row element, for its right-click / long-press menu.
  const [rootNodeEl, setRootNodeEl] = createContextMenuAnchor()

  // When the tree container shrinks (e.g. WorktreeOptions appearing below),
  // re-scroll the selected node into view if it was pushed out.
  onMount(() => {
    const observer = createRafResizeObserver(() => {
      if (!treeRef)
        return
      const selected = treeRef.querySelector(`.${styles.nodeSelected}`) as HTMLElement | null
      if (!selected)
        return
      const containerRect = treeRef.getBoundingClientRect()
      const nodeRect = selected.getBoundingClientRect()
      if (nodeRect.top < containerRect.top || nodeRect.bottom > containerRect.bottom) {
        treeRef.scrollTop += nodeRect.top - containerRect.top
      }
    })
    observer?.observe(treeRef)
    onCleanup(() => observer?.disconnect())
  })

  // -------------------------------------------------------------------------
  // Centralized tree state: expanded paths + children cache
  // -------------------------------------------------------------------------
  const [state, setState] = createStore<{
    expandedPaths: Record<string, boolean>
    childrenCache: Record<string, TreeNodeData[]>
    truncatedDirs: Record<string, number>
  }>({
    expandedPaths: {},
    childrenCache: {},
    truncatedDirs: {},
  })

  const [refreshVersion, setRefreshVersion] = createSignal(0)
  const triggerRefresh = () => setRefreshVersion(v => v + 1)

  // Expose imperative handle via ref callback.
  createEffect(() => {
    props.ref?.({
      collapseAll: () => {
        setState(produce((s) => {
          const rp = props.rootPath ?? '~'
          for (const key of Object.keys(s.expandedPaths)) {
            if (key !== rp)
              delete s.expandedPaths[key]
          }
        }))
      },
      refresh: triggerRefresh,
    })
  })

  const storageKey = () => `${PREFIX_DIRECTORY_TREE}${props.rootPath ?? '~'}:${props.showFiles ? 'files' : 'dirs'}`

  // Restore state from sessionStorage when rootPath changes
  createEffect(() => {
    const key = storageKey()
    try {
      const stored = sessionStorageGet<string>(key)
      if (stored) {
        const restored = deserializeState(stored)
        if (restored) {
          setState(restored)
          return
        }
      }
    }
    catch { /* ignore corrupt data */ }
    // Default: root is expanded
    setState({
      expandedPaths: { [props.rootPath ?? '~']: true },
      childrenCache: {},
      truncatedDirs: {},
    })
  })

  // Persist state whenever it changes
  createEffect(() => {
    // Read all to subscribe
    const expanded = state.expandedPaths
    const cache = state.childrenCache
    const truncated = state.truncatedDirs
    sessionStorageSet(storageKey(), serializeState(expanded, cache, truncated))
  })

  const isNodeExpanded = (path: string) => !!state.expandedPaths[path]
  const setNodeExpanded = (path: string, expanded: boolean) => {
    setState(produce((s) => {
      if (expanded) {
        s.expandedPaths[path] = true
      }
      else {
        delete s.expandedPaths[path]
      }
    }))
  }

  const getChildren = (path: string): TreeNodeData[] | undefined => state.childrenCache[path]
  // PRESENCE means truncated, not truthiness: the stored value is a count, and
  // a worker that does not report one stores 0. Testing the number would make
  // the notice vanish for exactly that case.
  const isTruncated = (path: string): boolean => state.truncatedDirs[path] !== undefined
  /** How many entries the worker saw before it cut; 0 when it did not say. */
  const truncatedTotal = (path: string): number => state.truncatedDirs[path] ?? 0
  // Every turn-end fans out into one loadChildren per expanded TreeNode.
  // Most subtrees haven't changed between turns, so skip the setState when
  // data and truncation match the cache — otherwise Solid would invalidate
  // children(), per-node gitIcon/diffStats, prefixIndex (walks every file ×
  // every ancestor), and downstream JSX for a subtree whose contents are
  // already on screen. Load-bearing; keep.
  const setChildrenInStore = (path: string, data: TreeNodeData[], truncated: boolean, totalEntries: number) => {
    const existing = state.childrenCache[path]
    // Compares the stored value INCLUDING its absence, so both "was it cut"
    // and "how much is missing" are covered: an unchanged listing whose total
    // moved (files added past the cut) still refreshes the notice.
    const truncationUnchanged = state.truncatedDirs[path] === (truncated ? totalEntries : undefined)
    if (truncationUnchanged && existing && sameTreeEntries(existing, data))
      return
    batch(() => {
      // Keyed reconcile, NOT a wholesale array replace. `<For>` maps by object
      // REFERENCE, so handing it a fresh array of fresh objects disposes and
      // re-creates EVERY row in the directory when a single entry changed --
      // and the three-dot menu lives INSIDE the row, so an open menu (and the
      // trigger being clicked) is torn out of the DOM under the pointer. One
      // file written by an agent during a turn was enough to do that to every
      // sibling at turn end. Reconciling by `path` mutates the survivors in
      // place, so only genuinely added/removed entries move.
      setState('childrenCache', path, reconcile(data, { key: 'path' }))
      setState('truncatedDirs', produce((t: Record<string, number>) => {
        if (truncated)
          t[path] = totalEntries
        else
          delete t[path]
      }))
    })
  }

  const workerFlavor = createMemo<PathFlavor>(() =>
    props.flavor ?? detectFlavor(props.homeDir || props.rootPath || ''))

  const rootPath = () => props.rootPath ?? '~'
  const rootDisplayName = () => {
    const rp = rootPath()
    return basename(rp, workerFlavor()) || rp
  }

  // Root children derived from the centralized cache, filtered and sorted the
  // same way every other directory is -- see visibleSortedChildren.
  const showHidden = () => props.showHiddenFiles ?? true
  const sortOrder = () => props.sortOrder ?? DEFAULT_FILE_SORT_ORDER
  const comparator = createMemo(() => makeFileComparator(sortOrder(), TREE_SORT_FIELDS))
  // Bound to the current sort here, so the root row and every TreeNode assemble
  // the notice one way instead of two. The context then carries the derived
  // value alone, not the raw sort key the comparator was already built from.
  const truncationNotice = (path: string, shown: number) =>
    formatTruncationNotice(shown, truncatedTotal(path), sortOrder().key)
  const rootChildren = createMemo(() => {
    const all = getChildren(rootPath())
    if (!all)
      return undefined
    return visibleSortedChildren(all, showHidden(), props.isVisible, comparator())
  })

  /**
   * The root row's own modification time, for its three-dot menu.
   *
   * Every other row reads its stat out of its parent's cached listing, but the
   * root has no parent in this tree, so it takes one StatFile of its own. That
   * is one call per root — not per row — and without it the root would be the
   * one directory whose menu showed nothing.
   */
  const [rootModTime, setRootModTime] = createSignal('')
  // Generation guard for the stat below. Two refreshes of the SAME root both
  // satisfy the worker/root match, so without it a slow first response can land
  // after a fast second one and pin the older time on the menu until the next
  // refresh. Same idiom as `loadVersion` on the root listing.
  let statVersion = 0
  createEffect(on(
    // `enabled` is a dependency, not just a guard: a worktree-creating agent
    // starts disabled and flips true once its directory exists, and the stat
    // has to run then rather than wait for a manual refresh.
    () => [props.workerId, rootPath(), refreshVersion(), props.enabled !== false] as const,
    ([workerId, root, , enabled], prev) => {
      // Blank the old value only when the ROW changes. A refresh re-stats the
      // same directory, so clearing there would make the menu's Modified row
      // blink out at every turn end.
      if (!prev || prev[0] !== workerId || prev[1] !== root)
        setRootModTime('')
      // Bumped BEFORE the early return, so a run that bails still invalidates
      // whatever the previous run left in flight.
      const version = ++statVersion
      if (!workerId || !enabled)
        return
      workerRpc.statFile(workerId, { workerId, path: root })
        .then((resp) => { // eslint-disable-line solid/reactivity -- one-shot async fetch
          if (version === statVersion && workerId === props.workerId && root === rootPath())
            setRootModTime(resp.info?.modTime ?? '')
        })
        .catch(() => { /* the menu simply omits the row */ })
    },
  ))

  // Load root children when workerId or rootPath changes
  createEffect(() => {
    const workerId = props.workerId
    const root = props.rootPath ?? '~'
    if (!workerId)
      return
    if (props.enabled === false)
      return

    // If we already have cached children (from sessionStorage or previous
    // load), skip fetching — this eliminates flicker on tab switches.
    if (getChildren(root) !== undefined)
      return

    const version = ++loadVersion
    setLoading(true)
    setError(null)
    loadChildren(workerId, root, props.showFiles ?? false)
      // eslint-disable-next-line solid/reactivity -- async promise callback; setChildrenInStore reads state as a current-value check, not a subscription
      .then((result) => {
        if (version !== loadVersion)
          return
        setChildrenInStore(root, result.entries, result.truncated, result.totalEntries)
        setLoading(false)
      })
      .catch((err) => {
        if (version !== loadVersion)
          return
        setError(formatErrorMessage(err, 'Failed to load directory'))
        setLoading(false)
      })
  })

  // Auto-refresh tree when an agent turn ends.
  createEffect(on(
    () => props.turnEndTrigger,
    (_, prev) => {
      if (prev !== undefined) {
        triggerRefresh()
      }
    },
  ))

  // Re-fetch root silently when refreshVersion bumps (keeps old data visible).
  createEffect(on(
    () => refreshVersion(),
    (_, prev) => {
      if (prev === undefined)
        return
      const workerId = props.workerId
      const root = props.rootPath ?? '~'
      if (!workerId)
        return
      loadChildren(workerId, root, props.showFiles ?? false)
        // eslint-disable-next-line solid/reactivity -- async promise callback; setChildrenInStore reads state as a current-value check, not a subscription
        .then((result) => {
          setChildrenInStore(root, result.entries, result.truncated, result.totalEntries)
        })
        .catch(() => { /* ignore refresh errors */ })
    },
  ))

  const rootDiffStats = createMemo<DiffStats | null>(() => {
    const store = props.gitStatusStore
    return store ? store.getNodeDiffStats(rootPath(), true) : null
  })

  const treeContextValue: TreeContextValue = {
    get workerId() { return props.workerId },
    get showFiles() { return props.showFiles ?? false },
    get rootPath() { return rootPath() },
    get homeDir() { return props.homeDir },
    flavor: workerFlavor,
    get scrollContainer() { return treeRef },
    get showHiddenFiles() { return showHidden() },
    comparator,
    truncationNotice,
    gitStatusStore: () => props.gitStatusStore,
    isVisible: () => props.isVisible,
    refreshVersion,
    onSelect: path => props.onSelect(path),
    get onFileOpen() { return props.onFileOpen },
    get onMention() { return props.onMention },
    get onOpenTerminal() { return props.onOpenTerminal },
    isNodeExpanded,
    setNodeExpanded,
    getChildren,
    setChildren: setChildrenInStore,
    isTruncated,
  }

  return (
    <TreeContext.Provider value={treeContextValue}>
      <div class={styles.container}>
        <div class={styles.tree} ref={treeRef}>
          <Switch fallback={(
            <div class={styles.treeInner}>
              {/* Root directory row */}
              <div
                ref={setRootNodeEl}
                class={styles.node}
                classList={{ [styles.nodeSelected]: props.selectedPath === rootPath() }}
                data-active={props.selectedPath === rootPath() ? 'true' : 'false'}
                style={{ 'padding-left': '8px' }}
                data-testid="tree-root-node"
                onClick={() => props.onSelect(rootPath())}
              >
                <Icon icon={FolderOpen} size="sm" class={styles.folderIcon} />
                <RowLabelWithStats
                  label={<span data-testid="tree-row-name" class={styles.nodeName}>{rootDisplayName()}</span>}
                  tooltipLabel={rootDisplayName()}
                  stats={rootDiffStats()}
                />
                <div class={sidebarActions}>
                  <TreeContextMenu
                    contextMenuFor={rootNodeEl}
                    path={rootPath()}
                    isDir
                    modTime={rootModTime()}
                  />
                </div>
              </div>
              <Show when={rootChildren() !== undefined}>
                <div class={`${styles.childrenWrapper} ${styles.childrenWrapperExpanded}`}>
                  <div class={styles.childrenInner}>
                    <Show
                      when={rootChildren()!.length > 0}
                      fallback={<div class={emptyState}>{props.isVisible ? 'No changes' : 'Empty directory'}</div>}
                    >
                      <For each={rootChildren()}>
                        {node => (
                          <TreeNode
                            node={node}
                            selectedPath={props.selectedPath}
                            depth={0}
                          />
                        )}
                      </For>
                      <Show when={isTruncated(rootPath()) && !props.isVisible}>
                        <div class={styles.emptyInline} style={{ 'padding-left': '24px' }}>
                          {truncationNotice(rootPath(), rootChildren()!.length)}
                        </div>
                      </Show>
                    </Show>
                  </div>
                </div>
              </Show>
            </div>
          )}
          >
            <Match when={error()}>
              <div class={styles.errorState}>{error()}</div>
            </Match>
            <Match when={props.enabled === false}>
              <div class={styles.loadingState} data-testid="directory-tree-starting">
                <StartupSpinner label="Starting…" />
              </div>
            </Match>
            <Match when={loading()}>
              <div class={styles.loadingState}>Loading...</div>
            </Match>
          </Switch>
        </div>
      </div>
    </TreeContext.Provider>
  )
}
