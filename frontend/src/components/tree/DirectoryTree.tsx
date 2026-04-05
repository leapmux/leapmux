import type { Component, JSX } from 'solid-js'
import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import type { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import AtSign from 'lucide-solid/icons/at-sign'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import ClipboardCopy from 'lucide-solid/icons/clipboard-copy'
import Copy from 'lucide-solid/icons/copy'
import File from 'lucide-solid/icons/file'
import FolderClosed from 'lucide-solid/icons/folder-closed'
import FolderOpen from 'lucide-solid/icons/folder-open'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import TerminalIcon from 'lucide-solid/icons/terminal'
import { createContext, createEffect, createSignal, For, on, onCleanup, onMount, Show, useContext } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { relativizePath, tildify } from '~/components/chat/messageUtils'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { IconButton } from '~/components/common/IconButton'
import { Tooltip } from '~/components/common/Tooltip'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import * as styles from './DirectoryTree.css'
import { DiffStatsBadge, getGitFileIconClass } from './gitStatusUtils'
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
  gitStatusStore?: ReturnType<typeof createGitFileStatusStore>
  /** When set, only show nodes whose paths are in this set. */
  visiblePaths?: Set<string>
  /** Signal bumped on agent turn-end; drives directory tree refresh. */
  turnEndTrigger?: number
  /** When false, entries with hidden=true are filtered out. Defaults to true. */
  showHiddenFiles?: boolean
  /** Ref callback for imperative actions (collapse all, etc.). */
  ref?: (handle: DirectoryTreeHandle) => void
}

interface TreeNodeData {
  path: string
  displayName: string
  isDir: boolean
  hidden: boolean
}

// -------------------------------------------------------------------------
// Tree context — bundles stable, tree-wide values to avoid prop drilling
// -------------------------------------------------------------------------

interface TreeContextValue {
  workerId: string
  showFiles: boolean
  rootPath: string
  homeDir?: string
  scrollContainer?: HTMLDivElement
  gitStatusStore: () => ReturnType<typeof createGitFileStatusStore> | undefined
  showHiddenFiles: boolean
  visiblePaths: () => Set<string> | undefined
  refreshVersion: () => number
  onSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  isNodeExpanded: (path: string) => boolean
  setNodeExpanded: (path: string, expanded: boolean) => void
  getChildren: (path: string) => TreeNodeData[] | undefined
  setChildren: (path: string, data: TreeNodeData[], truncated: boolean) => void
  isTruncated: (path: string) => boolean
}

const TreeContext = createContext<TreeContextValue>()

function useTree(): TreeContextValue {
  const ctx = useContext(TreeContext)
  if (!ctx)
    throw new Error('useTree must be used within a TreeContext.Provider')
  return ctx
}

// -------------------------------------------------------------------------
// Serialization helpers for sessionStorage
// -------------------------------------------------------------------------

interface DirectoryTreeStateJSON {
  expandedPaths: Record<string, boolean>
  childrenCache: Record<string, TreeNodeData[]>
  truncatedDirs?: Record<string, boolean>
}

function serializeState(
  expandedPaths: Record<string, boolean>,
  childrenCache: Record<string, TreeNodeData[]>,
  truncatedDirs: Record<string, boolean>,
): string {
  return JSON.stringify({ expandedPaths, childrenCache, truncatedDirs })
}

function deserializeState(raw: string): { expandedPaths: Record<string, boolean>, childrenCache: Record<string, TreeNodeData[]>, truncatedDirs: Record<string, boolean> } | null {
  try {
    const json: DirectoryTreeStateJSON = JSON.parse(raw)
    if (!json || typeof json !== 'object')
      return null
    return {
      expandedPaths: json.expandedPaths ?? {},
      childrenCache: json.childrenCache ?? {},
      truncatedDirs: json.truncatedDirs ?? {},
    }
  }
  catch {
    return null
  }
}

// -------------------------------------------------------------------------
// Visibility helpers
// -------------------------------------------------------------------------

/**
 * Check if a path is visible either directly or via an ancestor directory
 * entry in the visible set. Git reports untracked directories with a trailing
 * slash (e.g. "build/"), so when the tree merges single-child directories
 * (e.g. "build/bin"), walking up from the merged path finds the ancestor.
 */
function isPathVisible(path: string, visible: Set<string>): boolean {
  if (visible.has(path))
    return true
  let dir = path
  while (true) {
    const lastSlash = dir.lastIndexOf('/')
    if (lastSlash <= 0)
      return false
    dir = dir.substring(0, lastSlash)
    if (visible.has(`${dir}/`))
      return true
  }
}

// -------------------------------------------------------------------------
// File listing
// -------------------------------------------------------------------------

function sortEntries(a: FileInfo, b: FileInfo): number {
  if (a.isDir !== b.isDir)
    return a.isDir ? -1 : 1
  return a.name.localeCompare(b.name)
}

async function loadChildren(
  workerId: string,
  dirPath: string,
  showFiles: boolean,
): Promise<{ entries: TreeNodeData[], truncated: boolean }> {
  const resp = await workerRpc.listDirectory(workerId, { workerId, path: dirPath, maxDepth: 5, dirsOnly: !showFiles })
  const entries = resp.entries.toSorted(sortEntries)

  return {
    entries: entries.map(entry => ({
      path: entry.path,
      displayName: entry.name,
      isDir: entry.isDir,
      hidden: entry.hidden,
    })),
    truncated: resp.truncated,
  }
}

/** Three-dot context menu for a tree node (file or directory). */
const TreeContextMenu: Component<{
  path: string
  isDir: boolean
}> = (props) => {
  const tree = useTree()

  return (
    <DropdownMenu
      trigger={triggerProps => (
        <IconButton
          icon={MoreHorizontal}
          iconSize="xs"
          size="sm"
          class={menuTrigger}
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            triggerProps.onClick()
          }}
          ref={triggerProps.ref}
          onPointerDown={(e: PointerEvent) => {
            e.stopPropagation()
            triggerProps.onPointerDown()
          }}
          aria-expanded={triggerProps['aria-expanded']}
          data-testid="tree-context-button"
        />
      )}
    >
      <Show when={tree.onMention}>
        <button
          role="menuitem"
          data-testid="tree-mention-button"
          onClick={() => tree.onMention?.(props.path)}
        >
          <Icon icon={AtSign} size="sm" />
          Mention in chat
        </button>
      </Show>
      <Show when={props.isDir && tree.onOpenTerminal}>
        <button
          role="menuitem"
          data-testid="tree-open-terminal-button"
          onClick={() => tree.onOpenTerminal?.(props.path)}
        >
          <Icon icon={TerminalIcon} size="sm" />
          Open a terminal tab here
        </button>
      </Show>
      <button
        role="menuitem"
        data-testid="tree-copy-path-button"
        onClick={() => navigator.clipboard.writeText(props.path)}
      >
        <Icon icon={Copy} size="sm" />
        Copy path
      </button>
      <button
        role="menuitem"
        data-testid="tree-copy-relative-path-button"
        onClick={() => {
          const rel = props.path === tree.rootPath
            ? '.'
            : relativizePath(props.path, tree.rootPath, tree.homeDir)
          navigator.clipboard.writeText(rel)
        }}
      >
        <Icon icon={ClipboardCopy} size="sm" />
        Copy relative path
      </button>
    </DropdownMenu>
  )
}

/** Return a CSS class to color the file/folder icon based on git status. */
function getGitIconClass(
  node: TreeNodeData,
  gitStatusStore?: ReturnType<typeof createGitFileStatusStore>,
): { class: string, testId: string | undefined } {
  if (!gitStatusStore)
    return { class: '', testId: undefined }
  if (node.isDir) {
    const hasChanges = gitStatusStore.hasChanges(node.path)
    return hasChanges
      ? { class: styles.iconDirChanged, testId: undefined }
      : { class: '', testId: undefined }
  }
  const entry = gitStatusStore.getFileStatus(node.path)
  if (!entry)
    return { class: '', testId: undefined }
  return getGitFileIconClass(entry)
}

/** Render diff stats for a tree node (file or directory). */
function renderNodeDiffStats(
  node: TreeNodeData,
  gitStatusStore?: ReturnType<typeof createGitFileStatusStore>,
): JSX.Element {
  if (!gitStatusStore)
    return <></>
  if (node.isDir) {
    const stats = gitStatusStore.getDirDiffStats(node.path)
    return <DiffStatsBadge added={stats.added} deleted={stats.deleted} untracked={stats.untracked} />
  }
  const entry = gitStatusStore.getFileStatus(node.path)
  if (!entry)
    return <></>
  const isUntracked = entry.unstagedStatus === GitFileStatusCode.UNTRACKED
  return (
    <DiffStatsBadge
      added={isUntracked ? 0 : entry.linesAdded + entry.stagedLinesAdded}
      deleted={isUntracked ? 0 : entry.linesDeleted + entry.stagedLinesDeleted}
      untracked={isUntracked ? 1 : 0}
    />
  )
}

const TreeNode: Component<{
  node: TreeNodeData
  selectedPath: string
  depth: number
}> = (props) => {
  const tree = useTree()
  const [loading, setLoading] = createSignal(false)
  let wrapperRef!: HTMLDivElement
  let nodeRef!: HTMLDivElement
  let childrenRef: HTMLDivElement | undefined

  const expanded = () => tree.isNodeExpanded(props.node.path)
  const isSelected = () => props.selectedPath === props.node.path
  const allChildren = () => tree.getChildren(props.node.path) ?? []
  const children = () => {
    const all = allChildren()
    const showHidden = tree.showHiddenFiles
    const visible = tree.visiblePaths()
    if (showHidden && !visible)
      return all
    return all.filter(c =>
      (showHidden || !c.hidden)
      && (!visible || isPathVisible(c.path, visible)),
    )
  }
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
    const prefersReducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (prefersReducedMotion) {
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
      tree.setChildren(props.node.path, result.entries, result.truncated)
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
      if (!selected.startsWith(`${props.node.path}/`))
        return

      if (!loaded()) {
        doLoad().then(() => { // eslint-disable-line solid/reactivity -- one-shot async load
          tree.setNodeExpanded(props.node.path, true)
          // Scroll into view for the deepest auto-expanded node.
          // Only scroll if this is the closest ancestor (children will handle deeper).
          const hasMatchingChild = children().some(
            c => c.isDir && (selected.startsWith(`${c.path}/`) || selected === c.path),
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
          tree.setChildren(props.node.path, result.entries, result.truncated)
        })
        .catch(() => { /* ignore refresh errors */ })
    },
  ))

  // Scroll into view when this node is selected via path input.
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
  const gitIcon = () => getGitIconClass(props.node, tree.gitStatusStore())

  return (
    <div ref={wrapperRef}>
      <div
        ref={nodeRef}
        class={styles.node}
        classList={{ [styles.nodeSelected]: isSelected() }}
        style={{ 'padding-left': indent() }}
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
        <span class={props.node.hidden ? styles.nodeNameMuted : styles.nodeName}>{props.node.displayName}</span>
        {renderNodeDiffStats(props.node, tree.gitStatusStore())}
        <div class={sidebarActions}>
          <TreeContextMenu
            path={props.node.path}
            isDir={props.node.isDir}
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
            <Show when={tree.isTruncated(props.node.path) && !tree.visiblePaths()}>
              <div class={styles.emptyInline} style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}>
                {`${children().length}+ entries, listing truncated`}
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
  const [inputValue, setInputValue] = createSignal('')
  let loadVersion = 0
  let treeRef!: HTMLDivElement

  // When the tree container shrinks (e.g. WorktreeOptions appearing below),
  // re-scroll the selected node into view if it was pushed out.
  onMount(() => {
    const observer = new ResizeObserver(() => {
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
    observer.observe(treeRef)
    onCleanup(() => observer.disconnect())
  })

  // -------------------------------------------------------------------------
  // Centralized tree state: expanded paths + children cache
  // -------------------------------------------------------------------------
  const [state, setState] = createStore<{
    expandedPaths: Record<string, boolean>
    childrenCache: Record<string, TreeNodeData[]>
    truncatedDirs: Record<string, boolean>
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

  const storageKey = () => `directoryTree:state:${props.rootPath ?? '~'}:${props.showFiles ? 'files' : 'dirs'}`

  // Restore state from sessionStorage when rootPath changes
  createEffect(() => {
    const key = storageKey()
    try {
      const stored = sessionStorage.getItem(key)
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
    try {
      sessionStorage.setItem(storageKey(), serializeState(expanded, cache, truncated))
    }
    catch { /* quota exceeded — ignore */ }
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
  const isTruncated = (path: string): boolean => !!state.truncatedDirs[path]
  const setChildrenInStore = (path: string, data: TreeNodeData[], truncated: boolean) => {
    setState(produce((s) => {
      s.childrenCache[path] = data
      if (truncated) {
        s.truncatedDirs[path] = true
      }
      else {
        delete s.truncatedDirs[path]
      }
    }))
  }

  const rootPath = () => props.rootPath ?? '~'
  const rootDisplayName = () => {
    const rp = rootPath()
    return rp.split('/').pop() || rp
  }

  // Root children derived from the centralized cache, optionally filtered.
  const showHidden = () => props.showHiddenFiles ?? true
  const rootChildren = () => {
    const all = getChildren(rootPath())
    if (!all)
      return undefined
    const sh = showHidden()
    const visible = props.visiblePaths
    if (sh && !visible)
      return all
    return all.filter(c =>
      (sh || !c.hidden)
      && (!visible || isPathVisible(c.path, visible)),
    )
  }

  // Sync external selectedPath to input (tildified for display)
  createEffect(() => {
    setInputValue(tildify(props.selectedPath, props.homeDir))
  })

  // Load root children when workerId or rootPath changes
  createEffect(() => {
    const workerId = props.workerId
    const root = props.rootPath ?? '~'
    if (!workerId)
      return

    // If we already have cached children (from sessionStorage or previous
    // load), skip fetching — this eliminates flicker on tab switches.
    if (getChildren(root) !== undefined)
      return

    const version = ++loadVersion
    setLoading(true)
    setError(null)
    loadChildren(workerId, root, props.showFiles ?? false)
      .then((result) => {
        if (version !== loadVersion)
          return
        setChildrenInStore(root, result.entries, result.truncated)
        setLoading(false)
      })
      .catch((err) => {
        if (version !== loadVersion)
          return
        setError(err instanceof Error ? err.message : 'Failed to load directory')
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
        .then((result) => {
          setChildrenInStore(root, result.entries, result.truncated)
        })
        .catch(() => { /* ignore refresh errors */ })
    },
  ))

  const handlePathKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      const value = inputValue().trim()
      if (value) {
        props.onSelect(value)
      }
    }
  }

  const handlePathBlur = () => {
    const value = inputValue().trim()
    if (!value)
      return
    // Only emit onSelect if the user actually edited the path.
    // The input displays a tildified version, so re-emitting on blur
    // would change the signal (e.g. "/home/user/repo" → "~/repo"),
    // causing downstream effects to refire unnecessarily.
    if (value === tildify(props.selectedPath, props.homeDir))
      return
    props.onSelect(value)
  }

  const treeContextValue: TreeContextValue = {
    get workerId() { return props.workerId },
    get showFiles() { return props.showFiles ?? false },
    get rootPath() { return rootPath() },
    get homeDir() { return props.homeDir },
    get scrollContainer() { return treeRef },
    get showHiddenFiles() { return showHidden() },
    gitStatusStore: () => props.gitStatusStore,
    visiblePaths: () => props.visiblePaths,
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
        <div class={styles.pathInput}>
          <Tooltip text={props.selectedPath}>
            <input
              class={styles.pathInputField}
              type="text"
              value={inputValue()}
              onInput={e => setInputValue(e.currentTarget.value)}
              onKeyDown={handlePathKeyDown}
              onBlur={handlePathBlur}
              placeholder="Enter path..."
            />
          </Tooltip>
        </div>
        <div class={styles.tree} ref={treeRef}>
          <Show when={error()}>
            <div class={styles.errorState}>{error()}</div>
          </Show>
          <Show when={loading()}>
            <div class={styles.loadingState}>Loading...</div>
          </Show>
          <Show when={!loading() && !error()}>
            <div class={styles.treeInner}>
              {/* Root directory row */}
              <div
                class={styles.node}
                classList={{ [styles.nodeSelected]: props.selectedPath === rootPath() }}
                style={{ 'padding-left': '8px' }}
                data-testid="tree-root-node"
                onClick={() => props.onSelect(rootPath())}
              >
                <Icon icon={FolderOpen} size="sm" class={styles.folderIcon} />
                <span class={styles.nodeName}>{rootDisplayName()}</span>
                {renderNodeDiffStats({ path: rootPath(), displayName: rootDisplayName(), isDir: true, hidden: false }, props.gitStatusStore)}
                <div class={sidebarActions}>
                  <TreeContextMenu
                    path={rootPath()}
                    isDir
                  />
                </div>
              </div>
              <Show when={rootChildren() !== undefined}>
                <div class={`${styles.childrenWrapper} ${styles.childrenWrapperExpanded}`}>
                  <div class={styles.childrenInner}>
                    <Show
                      when={rootChildren()!.length > 0}
                      fallback={<div class={styles.emptyState}>{props.visiblePaths ? 'No changes' : 'Empty directory'}</div>}
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
                      <Show when={isTruncated(rootPath()) && !props.visiblePaths}>
                        <div class={styles.emptyInline} style={{ 'padding-left': '24px' }}>
                          {`${rootChildren()!.length}+ entries, listing truncated`}
                        </div>
                      </Show>
                    </Show>
                  </div>
                </div>
              </Show>
            </div>
          </Show>
        </div>
      </div>
    </TreeContext.Provider>
  )
}
