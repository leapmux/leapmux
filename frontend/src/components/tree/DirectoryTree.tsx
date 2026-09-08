import type { Accessor, Component } from 'solid-js'
import type { DirectoryListingData } from './directoryListings'
import type { DirectoryTreeStateJSON, TreeNodeData } from './directoryTreeState'
import type { FileSortOrder } from '~/lib/fileSort'
import type { PathFlavor } from '~/lib/paths'
import type { createRepoGitStore, DiffStats } from '~/stores/repoGit.store'
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
import { DEFAULT_FILE_SORT_ORDER, makeFileComparator } from '~/lib/fileSort'
import { basename, detectFlavor, isFilesystemRoot } from '~/lib/paths'
import { prefersReducedMotion } from '~/lib/prefersReducedMotion'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { emptyState, warningText } from '~/styles/shared.css'
import { createDirectoryListings } from './directoryListings'
import * as styles from './DirectoryTree.css'
import {
  deserializeState,
  formatTruncationNotice,
  isDescendantPath,
  samePath,
  sameTreeEntries,
  serializeState,
  TREE_SORT_FIELDS,
  visibleSortedChildren,
} from './directoryTreeState'
import { getGitFileIconClass, RowLabelWithStats } from './gitStatusUtils'
import { menuTrigger, sidebarActions } from './sidebarActions.css'

export interface DirectoryTreeHandle {
  collapseAll: () => void
  refresh: () => void
  /**
   * Open the node at `path` itself.
   *
   * The reveal effect opens every ANCESTOR of the reveal target and stops
   * there: its `isDescendantPath` test is strict, so the target's own node
   * never expands. A caller that means "go to this directory and show what is
   * inside it" takes this second step.
   *
   * Call it AFTER the write that selects `path`. That write can re-root the
   * tree, a new root replaces the whole expansion state, and the reverse order
   * therefore loses the expansion.
   */
  expandPath: (path: string) => void
}

export interface DirectoryTreeProps {
  workerId: string
  showFiles?: boolean
  selectedPath: string
  onSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  /**
   * The tree's root row. REQUIRED, and an absolute path.
   *
   * There is deliberately no `'~'` default. A tilde resolves only on the
   * WORKER, so nothing here can reason about it: `basename('~')` is `'~'`,
   * `isAbsolute('~')` is false, and `relativeUnder(abs, '~')` is null -- which
   * silently changes what "Copy relative path" answers and what counts as a
   * descendant. A caller whose own root is not known yet renders nothing
   * instead of passing a placeholder.
   */
  rootPath: string
  /**
   * Where to walk the tree open when NOTHING is selected.
   *
   * The picker opens on the filesystem root so a user can reach anywhere, and
   * "New workspace" started from a directory opens with no selection at all.
   * Without this the first thing that user sees is `/`, with their home
   * directory several clicks away. Ignored the moment `selectedPath` is
   * non-empty, so it never fights the user's own choice.
   *
   * EXPANSION ONLY. Selection is `selectedPath`, and nothing else reads this,
   * so a revealed node is open but never selected.
   */
  revealPath?: string
  homeDir?: string
  /**
   * Path flavor for the worker this tree is rendering. Defaults to a
   *  best-effort sniff from homeDir/rootPath.
   */
  flavor?: PathFlavor
  gitStatusStore: ReturnType<typeof createRepoGitStore>
  /**
   * When false, skip git change icons and diff-stat annotations on tree rows.
   * The directory picker passes false: the shared store is keyed to the
   * focused tab's repo, not the path being browsed.
   */
  showGitStatus?: boolean
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

// -------------------------------------------------------------------------
// Tree context — bundles stable, tree-wide values to avoid prop drilling
// -------------------------------------------------------------------------

interface TreeContextValue {
  workerId: string
  showFiles: boolean
  rootPath: string
  /**
   * The path the tree walks itself open toward: the user's selection when
   * there is one, else `revealPath`. ONE accessor, so no node restates the
   * precedence and the chain loader and the per-node cascade share one input.
   */
  revealTarget: () => string
  homeDir?: string
  flavor: () => PathFlavor
  scrollContainer?: HTMLDivElement
  gitStatusStore: () => ReturnType<typeof createRepoGitStore>
  showGitStatus: boolean
  showHiddenFiles: boolean
  /** Comparator for the current sort order, shared by every node. */
  comparator: () => (a: TreeNodeData, b: TreeNodeData) => number
  /** The inline notice for a directory the worker truncated, bound to the current sort. */
  truncationNotice: (path: string, shown: number) => string
  /**
   * Why a directory would not list, or undefined when it listed.
   *
   * The one surface for a per-node listing failure. The tree's own error slot
   * belongs to the FIRST load, when there is nothing on screen at all, so
   * without this a directory the caller cannot read simply refused to open
   * and said nothing.
   */
  unreadableReason: (path: string) => string | undefined
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
  /**
   * Fetch and cache one directory's children, unless a request that will
   * supply them is already in flight -- in which case await that one.
   *
   * The de-duplication is the point. The chain loader and the per-node cascade
   * both react to the same reveal target, so on a selection change they would
   * otherwise ask for the same directories at the same moment. Never rejects:
   * a listing that fails leaves the node collapsed, exactly as before.
   */
  ensureChildren: (path: string) => Promise<void>
  /**
   * Fetch one directory's children again, whatever is already in flight.
   *
   * The refresh path, and only that: a user who presses Refresh has said the
   * cached answer is stale, so awaiting a claim made before that would return
   * exactly the answer they rejected.
   */
  refetchChildren: (path: string) => Promise<void>
  isTruncated: (path: string) => boolean
}

const TreeContext = createStableContext<TreeContextValue>('tree/DirectoryTree')

function useTree(): TreeContextValue {
  const ctx = useContext(TreeContext)
  if (!ctx)
    throw new Error('useTree must be used within a TreeContext.Provider')
  return ctx
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

  // Loads this node's children, and reports whether they arrived.
  //
  // The ANSWER is what stops a failure from spinning. A directory the caller
  // cannot read -- and a tree rooted at `/` puts several in front of every
  // user -- fails every time it is asked. Expanding it anyway leaves the
  // "expanded but the cache is missing" effect below permanently satisfied,
  // and that effect reads `loading()`, so each failure re-triggers it: one
  // ListDirectory per round trip, for as long as the tree is on screen.
  const doLoad = async (): Promise<boolean> => {
    if (loaded())
      return true
    if (loading())
      return false
    setLoading(true)
    try {
      // Through the tree, not straight to the RPC: the chain loader claims
      // every directory it is about to fetch, so a node revealed by the same
      // selection change AWAITS that one request instead of racing it with a
      // request of its own. Without that, revealing a path the tree already
      // has on screen costs one request per level again -- the exact cost the
      // chain exists to remove.
      await tree.ensureChildren(props.node.path)
      return loaded()
    }
    finally {
      setLoading(false)
    }
  }

  // Why this node's listing would not load, or undefined when it loaded.
  //
  // Read from the tree's store rather than held in a local signal: the reason
  // is also what the row RENDERS, and one statement of it means the "do not
  // ask again" mark and the message on screen cannot disagree. A refresh
  // clears it, which is the user's own "try again".
  const loadError = () => tree.unreadableReason(props.node.path)

  const toggle = async () => {
    if (!props.node.isDir) {
      tree.onSelect(props.node.path)
      tree.onFileOpen?.(props.node.path)
      return
    }
    const ok = await doLoad()
    // A directory that would not list does not expand: expanding it arms the
    // re-fetch effect against a request that fails every time. Collapsing is
    // always allowed, so this gates the OPEN direction alone.
    const willExpand = !expanded() && ok

    // Set expanded state before onSelect so that the scroll-on-select
    // effect sees the correct state and skips scrolling on collapse.
    tree.setNodeExpanded(props.node.path, willExpand)
    tree.onSelect(props.node.path)
    if (willExpand) {
      scrollIntoViewIfNeeded()
    }
  }

  // Auto-expand when the reveal target moves under this node. The target is
  // the user's selection when there is one and `revealPath` when there is not
  // -- see `DirectoryTreeProps.revealPath`.
  //
  // The tree-level chain effect normally caches every ancestor before these
  // nodes mount, so this takes the `loaded()` branch below and expands with no
  // round trip. It stays the fallback for a chain request that failed, and it
  // owns the deepest-node scroll, which the scroll-on-select effect further
  // down does not cover: that one returns early for a collapsed directory, and
  // the reveal target is never auto-expanded by its own node.
  createEffect(on(
    () => tree.revealTarget(),
    (target) => {
      if (!props.node.isDir)
        return
      const flavor = tree.flavor()
      if (!isDescendantPath(target, props.node.path, flavor))
        return

      if (!loaded()) {
        doLoad().then((ok) => { // eslint-disable-line solid/reactivity -- one-shot async load
          // A listing that failed leaves the node collapsed. Expanding it
          // would arm the re-fetch effect below against a request that fails
          // every time -- see `doLoad`.
          if (!ok)
            return
          tree.setNodeExpanded(props.node.path, true)
          // Scroll into view for the deepest auto-expanded node.
          // Only scroll if this is the closest ancestor (children will handle deeper).
          const hasMatchingChild = children().some(
            c => c.isDir && (isDescendantPath(target, c.path, flavor) || samePath(target, c.path, flavor)),
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

  // Re-fetch when expanded but cache is missing (e.g. after sessionStorage
  // restore). Never after a failure: this effect reads `loading()`, so a
  // directory that cannot be read would re-arm it on every rejection.
  createEffect(() => {
    if (props.node.isDir && expanded() && !loaded() && !loading() && !loadError()) {
      void doLoad()
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
      // Through the tree, not straight to the RPC, so the refresh carries the
      // worker guard: an answer that lands after the tree moved to another
      // worker must not write the previous worker's listing into it.
      //
      // `refetchChildren`, never `ensureChildren`: a pending claim's answer is
      // the one the user just called stale, so the de-duplicating form would
      // swallow the refresh entirely.
      void tree.refetchChildren(props.node.path)
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
    if (!tree.showGitStatus)
      return NO_GIT_ICON
    const store = tree.gitStatusStore()
    if (props.node.isDir) {
      return store.hasChanges(props.node.path)
        ? { class: styles.iconDirChanged, testId: undefined }
        : NO_GIT_ICON
    }
    const entry = store.getFileStatus(props.node.path)
    return entry ? getGitFileIconClass(entry) : NO_GIT_ICON
  })
  const diffStats = createMemo<DiffStats | null>(() => {
    if (!tree.showGitStatus)
      return null
    return tree.gitStatusStore().getNodeDiffStats(props.node.path, props.node.isDir)
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
      {/*
        Beside the loading row, NOT inside the children block below: a listing
        that failed leaves the node collapsed, so a reason rendered among the
        children it does not have would never be seen. This is the one surface
        for a per-node failure -- the tree's own error slot belongs to the
        first load, when nothing is on screen at all.
      */}
      <Show when={!loading() && loadError()}>
        {reason => (
          <div
            class={`${styles.emptyInline} ${warningText}`}
            style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}
            data-testid="tree-node-error"
          >
            {reason()}
          </div>
        )}
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
    /**
     * Why a directory would not list, by path.
     *
     * Deliberately NOT in the persisted payload. It describes the last
     * ATTEMPT, not the cached contents, and a reason restored from a previous
     * session would sit on a row that lists perfectly well now. It is also the
     * "do not ask again" mark: the re-fetch effect skips a path that has one,
     * so a directory the caller cannot read is asked about once per Refresh
     * rather than once per round trip.
     */
    unreadableDirs: Record<string, string>
  }>({
    expandedPaths: {},
    childrenCache: {},
    truncatedDirs: {},
    unreadableDirs: {},
  })

  const [refreshVersion, setRefreshVersion] = createSignal(0)
  const triggerRefresh = () => setRefreshVersion(v => v + 1)

  // The workerId is part of the key because two workers routinely share a root
  // path -- every POSIX worker roots the picker at `/`. Without it the restore
  // below hydrates worker A's listing for worker B, and the load effect then
  // skips its fetch because the cache is populated, so the picker shows the
  // wrong machine's directories with no way to tell.
  const storageKey = () => `${PREFIX_DIRECTORY_TREE}${props.workerId}:${props.rootPath}:${props.showFiles ? 'files' : 'dirs'}`

  // Restore state from sessionStorage when rootPath changes
  createEffect(() => {
    const key = storageKey()
    try {
      const stored = sessionStorageGet<DirectoryTreeStateJSON>(key)
      if (stored) {
        const restored = deserializeState(stored)
        if (restored) {
          setState({ ...restored, unreadableDirs: {} })
          return
        }
      }
    }
    catch { /* ignore corrupt data */ }
    // Default: root is expanded
    setState({
      expandedPaths: { [props.rootPath]: true },
      childrenCache: {},
      truncatedDirs: {},
      unreadableDirs: {},
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

  // Expose imperative handle via ref callback.
  createEffect(() => {
    props.ref?.({
      collapseAll: () => {
        setState(produce((s) => {
          const rp = props.rootPath
          for (const key of Object.keys(s.expandedPaths)) {
            if (key !== rp)
              delete s.expandedPaths[key]
          }
        }))
      },
      refresh: triggerRefresh,
      // An empty path names no node. Writing one would persist a key that
      // matches nothing and that no chevron can ever collapse again.
      expandPath: (path: string) => {
        if (path)
          setNodeExpanded(path, true)
      },
    })
  })

  const getChildren = (path: string): TreeNodeData[] | undefined => state.childrenCache[path]
  // PRESENCE means truncated, not truthiness: the stored value is a count, and
  // a worker that does not report one stores 0. Testing the number would make
  // the notice vanish for exactly that case.
  /** Why this directory would not list, or undefined when it listed. */
  const unreadableReason = (path: string): string | undefined => state.unreadableDirs[path]
  const setUnreadable = (path: string, reason: string | undefined) => {
    if (state.unreadableDirs[path] === reason)
      return
    setState('unreadableDirs', produce((u: Record<string, string>) => {
      if (reason === undefined)
        delete u[path]
      else
        u[path] = reason
    }))
  }

  const isTruncated = (path: string): boolean => state.truncatedDirs[path] !== undefined
  /** How many entries the worker saw before it cut; 0 when it did not say. */
  const truncatedTotal = (path: string): number => state.truncatedDirs[path] ?? 0
  // Every turn-end fans out into one listing request per expanded TreeNode.
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
    // A listing that arrived clears the reason: whatever stopped it before,
    // it does not stop it now.
    setUnreadable(path, undefined)
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

  /**
   * Write a whole root-to-target chain in ONE reactive pass.
   *
   * Per listing it does exactly what `setChildrenInStore` does, by calling it:
   * the same unchanged-content fast path, the same keyed reconcile, the same
   * truncation bookkeeping. The difference is the single `batch`. Separate
   * calls would invalidate `children()`, every node's git decorations and the
   * whole `<For>` once per level, and the passes in between would render a
   * tree whose ancestors are loaded and whose descendants are not.
   */
  const setListingsInStore = (listings: readonly DirectoryListingData[]) => {
    batch(() => {
      for (const listing of listings)
        setChildrenInStore(listing.path, listing.entries, listing.truncated, listing.totalEntries)
    })
  }

  const workerFlavor = createMemo<PathFlavor>(() =>
    props.flavor ?? detectFlavor(props.homeDir || props.rootPath || ''))

  const rootPath = () => props.rootPath
  const revealTarget = () => props.selectedPath || props.revealPath || ''
  // Everything about WHEN to ask the worker, and for what, lives in the
  // loader. This component owns the store it writes into, and the rows.
  const listings = createDirectoryListings({
    workerId: () => props.workerId,
    rootPath: () => props.rootPath,
    showFiles: () => props.showFiles ?? false,
    flavor: () => workerFlavor(),
    enabled: () => props.enabled !== false,
    // The key of the cache the loader speaks for. When it changes the store is
    // replaced wholesale, so every claim and the chain guard go with it -- see
    // `DirectoryListingsOptions.cacheKey`.
    cacheKey: storageKey,
    revealTarget,
    getChildren,
    setChildren: setChildrenInStore,
    setListings: setListingsInStore,
    setUnreadable,
  })
  const { ensureChildren, refetchChildren, loading, error } = listings

  const rootDisplayName = () => {
    const rp = rootPath()
    const flavor = workerFlavor()
    // A FILESYSTEM ROOT has no basename worth showing: `basename('/')` is ''
    // and `basename('C:\\')` is 'C:' -- the drive without the separator that
    // makes it a root, and not what a drive selector beside it reads. Label
    // the root with itself.
    //
    // `isFilesystemRoot`, not a comparison against `filesystemRoot`'s output:
    // a root arrives spelled several ways (`C:/` as well as `C:\`), and only
    // one of them equals that output.
    if (isFilesystemRoot(rp, flavor))
      return rp
    return basename(rp, flavor) || rp
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
      // Refresh is "everything you know is stale", and that covers what the
      // tree knows about a directory it could NOT read. A node whose listing
      // failed stays collapsed, so nothing re-lists it and its reason would
      // otherwise sit on the row for the rest of the session. Clearing it also
      // lifts the "do not ask again" mark, so the next click on that row
      // retries rather than showing the old message again.
      // `produce`, not `setState('unreadableDirs', {})`: a store write MERGES
      // the object it is given, so an empty one changes nothing at all.
      setState('unreadableDirs', produce((u: Record<string, string>) => {
        for (const path of Object.keys(u))
          delete u[path]
      }))

      const workerId = props.workerId
      const root = props.rootPath
      if (!workerId)
        return
      // Same form as the per-node refresh above, and for the same two
      // reasons: the worker guard, and a refresh that must ask again rather
      // than await a claim it has just declared stale.
      void refetchChildren(root)
    },
  ))

  const rootDiffStats = createMemo<DiffStats | null>(() => {
    if (props.showGitStatus === false)
      return null
    return props.gitStatusStore.getNodeDiffStats(rootPath(), true)
  })

  const treeContextValue: TreeContextValue = {
    get workerId() { return props.workerId },
    get showFiles() { return props.showFiles ?? false },
    get rootPath() { return rootPath() },
    revealTarget,
    get homeDir() { return props.homeDir },
    flavor: workerFlavor,
    get scrollContainer() { return treeRef },
    get showHiddenFiles() { return showHidden() },
    comparator,
    truncationNotice,
    unreadableReason,
    gitStatusStore: () => props.gitStatusStore,
    get showGitStatus() { return props.showGitStatus !== false },
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
    ensureChildren,
    refetchChildren,
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
