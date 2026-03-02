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
import { createEffect, createSignal, For, on, Show } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import { fileClient } from '~/api/clients'
import { relativizePath, tildify } from '~/components/chat/messageUtils'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { IconButton } from '~/components/common/IconButton'
import { GitFileStatusCode } from '~/generated/leapmux/v1/git_pb'
import * as styles from './DirectoryTree.css'
import * as fsStyles from './FilesSection.css'

export interface DirectoryTreeHandle {
  collapseAll: () => void
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
  /** Ref callback for imperative actions (collapse all, etc.). */
  ref?: (handle: DirectoryTreeHandle) => void
}

interface TreeNodeData {
  path: string
  displayName: string
  isDir: boolean
  size: bigint
}

// -------------------------------------------------------------------------
// Serialization helpers for sessionStorage (BigInt ↔ string)
// -------------------------------------------------------------------------

interface TreeNodeDataJSON {
  path: string
  displayName: string
  isDir: boolean
  size: string
}

interface DirectoryTreeStateJSON {
  expandedPaths: Record<string, boolean>
  childrenCache: Record<string, TreeNodeDataJSON[]>
}

function serializeState(
  expandedPaths: Record<string, boolean>,
  childrenCache: Record<string, TreeNodeData[]>,
): string {
  const json: DirectoryTreeStateJSON = {
    expandedPaths,
    childrenCache: {},
  }
  for (const [key, nodes] of Object.entries(childrenCache)) {
    json.childrenCache[key] = nodes.map(n => ({
      path: n.path,
      displayName: n.displayName,
      isDir: n.isDir,
      size: n.size.toString(),
    }))
  }
  return JSON.stringify(json)
}

function deserializeState(raw: string): { expandedPaths: Record<string, boolean>, childrenCache: Record<string, TreeNodeData[]> } | null {
  try {
    const json: DirectoryTreeStateJSON = JSON.parse(raw)
    if (!json || typeof json !== 'object')
      return null
    const childrenCache: Record<string, TreeNodeData[]> = {}
    if (json.childrenCache) {
      for (const [key, nodes] of Object.entries(json.childrenCache)) {
        childrenCache[key] = nodes.map(n => ({
          path: n.path,
          displayName: n.displayName,
          isDir: n.isDir,
          size: BigInt(n.size),
        }))
      }
    }
    return {
      expandedPaths: json.expandedPaths ?? {},
      childrenCache,
    }
  }
  catch {
    return null
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
): Promise<TreeNodeData[]> {
  const resp = await fileClient.listDirectory({ workerId, path: dirPath, maxDepth: 5 })
  const entries = [...resp.entries].sort(sortEntries)
  const filtered = showFiles ? entries : entries.filter(e => e.isDir)

  return filtered.map(entry => ({
    path: entry.path,
    displayName: entry.name,
    isDir: entry.isDir,
    size: entry.size,
  }))
}

/** Renders the three-dot context menu for a tree node (file or directory). */
function renderTreeContextMenu(menuProps: {
  path: string
  isDir: boolean
  rootPath: string
  homeDir?: string
  onMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
}): JSX.Element {
  let popoverRef: HTMLElement | undefined
  const closeMenu = () => popoverRef?.hidePopover()

  return (
    <DropdownMenu
      trigger={triggerProps => (
        <IconButton
          icon={MoreHorizontal}
          iconSize="xs"
          size="sm"
          class={styles.nodeMenuTrigger}
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            triggerProps.onClick()
          }}
          ref={triggerProps.ref}
          onPointerDown={(e: PointerEvent) => {
            e.stopPropagation()
            triggerProps.onPointerDown(e)
          }}
          aria-expanded={triggerProps['aria-expanded']}
          data-testid="tree-context-button"
        />
      )}
      popoverRef={(el) => { popoverRef = el }}
    >
      <Show when={menuProps.onMention}>
        <button
          role="menuitem"
          data-testid="tree-mention-button"
          onClick={() => {
            menuProps.onMention?.(menuProps.path)
            closeMenu()
          }}
        >
          <Icon icon={AtSign} size="sm" />
          Mention in chat
        </button>
      </Show>
      <Show when={menuProps.isDir && menuProps.onOpenTerminal}>
        <button
          role="menuitem"
          data-testid="tree-open-terminal-button"
          onClick={() => {
            menuProps.onOpenTerminal?.(menuProps.path)
            closeMenu()
          }}
        >
          <Icon icon={TerminalIcon} size="sm" />
          Open a terminal tab here
        </button>
      </Show>
      <button
        role="menuitem"
        data-testid="tree-copy-path-button"
        onClick={() => {
          navigator.clipboard.writeText(menuProps.path)
          closeMenu()
        }}
      >
        <Icon icon={Copy} size="sm" />
        Copy path
      </button>
      <button
        role="menuitem"
        data-testid="tree-copy-relative-path-button"
        onClick={() => {
          const rel = menuProps.path === menuProps.rootPath
            ? '.'
            : relativizePath(menuProps.path, menuProps.rootPath, menuProps.homeDir)
          navigator.clipboard.writeText(rel)
          closeMenu()
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
  if (entry.unstagedStatus === GitFileStatusCode.UNMERGED || entry.stagedStatus === GitFileStatusCode.UNMERGED)
    return { class: styles.iconConflict, testId: 'git-status-unstaged' }
  if (entry.unstagedStatus === GitFileStatusCode.UNTRACKED)
    return { class: styles.iconUntracked, testId: 'git-status-untracked' }
  if (entry.stagedStatus !== GitFileStatusCode.UNSPECIFIED && entry.unstagedStatus === GitFileStatusCode.UNSPECIFIED)
    return { class: styles.iconStaged, testId: 'git-status-staged' }
  return { class: styles.iconUnstaged, testId: 'git-status-unstaged' }
}

/** Render diff stats for a tree node (file only). */
function renderNodeDiffStats(
  node: TreeNodeData,
  gitStatusStore?: ReturnType<typeof createGitFileStatusStore>,
): JSX.Element {
  if (!gitStatusStore || node.isDir)
    return <></>
  const entry = gitStatusStore.getFileStatus(node.path)
  if (!entry)
    return <></>
  const totalAdded = entry.linesAdded + entry.stagedLinesAdded
  const totalDeleted = entry.linesDeleted + entry.stagedLinesDeleted
  if (totalAdded === 0 && totalDeleted === 0)
    return <></>
  return (
    <span class={fsStyles.diffStats} data-testid="git-diff-stats">
      <Show when={totalAdded > 0}>
        <span class={fsStyles.diffStatsAdded}>
          +
          {totalAdded}
        </span>
      </Show>
      {totalAdded > 0 && totalDeleted > 0 ? ' ' : ''}
      <Show when={totalDeleted > 0}>
        <span class={fsStyles.diffStatsDeleted}>
          -
          {totalDeleted}
        </span>
      </Show>
    </span>
  )
}

const TreeNode: Component<{
  node: TreeNodeData
  workerId: string
  showFiles: boolean
  selectedPath: string
  onSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  depth: number
  scrollContainer?: HTMLDivElement
  rootPath: string
  homeDir?: string
  isNodeExpanded: (path: string) => boolean
  setNodeExpanded: (path: string, expanded: boolean) => void
  getChildren: (path: string) => TreeNodeData[] | undefined
  setChildren: (path: string, data: TreeNodeData[]) => void
  gitStatusStore?: ReturnType<typeof createGitFileStatusStore>
  visiblePaths?: Set<string>
}> = (props) => {
  const [loading, setLoading] = createSignal(false)
  let wrapperRef!: HTMLDivElement

  const expanded = () => props.isNodeExpanded(props.node.path)
  const isSelected = () => props.selectedPath === props.node.path
  const allChildren = () => props.getChildren(props.node.path) ?? []
  const children = () => {
    const visible = props.visiblePaths
    if (!visible)
      return allChildren()
    return allChildren().filter(c => visible.has(c.path))
  }
  const loaded = () => props.getChildren(props.node.path) !== undefined

  const scrollIntoViewIfNeeded = () => {
    const container = props.scrollContainer
    if (!container || !wrapperRef)
      return
    requestAnimationFrame(() => {
      const containerRect = container.getBoundingClientRect()
      const wrapperRect = wrapperRef.getBoundingClientRect()
      if (wrapperRect.bottom > containerRect.bottom) {
        container.scrollTop += wrapperRect.top - containerRect.top
      }
    })
  }

  const doLoad = async () => {
    if (loaded() || loading())
      return
    setLoading(true)
    try {
      const result = await loadChildren(props.workerId, props.node.path, props.showFiles)
      props.setChildren(props.node.path, result)
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
      props.onSelect(props.node.path)
      props.onFileOpen?.(props.node.path)
      return
    }
    await doLoad()
    const willExpand = !expanded()
    // Set expanded state before onSelect so that the scroll-on-select
    // effect sees the correct state and skips scrolling on collapse.
    props.setNodeExpanded(props.node.path, willExpand)
    props.onSelect(props.node.path)
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
          props.setNodeExpanded(props.node.path, true)
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
        props.setNodeExpanded(props.node.path, true)
      }
    },
  ))

  // Scroll into view when this node is selected via path input.
  // Skip for directories that are collapsed — collapsing should not scroll.
  createEffect(() => {
    if (props.selectedPath === props.node.path && wrapperRef) {
      if (props.node.isDir && !expanded())
        return
      const container = props.scrollContainer
      if (!container)
        return
      requestAnimationFrame(() => {
        const containerRect = container.getBoundingClientRect()
        const wrapperRect = wrapperRef.getBoundingClientRect()
        if (wrapperRect.top < containerRect.top || wrapperRect.bottom > containerRect.bottom) {
          container.scrollTop += wrapperRect.top - containerRect.top
        }
      })
    }
  })

  const indent = () => `${8 + props.depth * 16}px`
  const gitIcon = () => getGitIconClass(props.node, props.gitStatusStore)

  return (
    <div ref={wrapperRef}>
      <div
        class={`${styles.node} ${isSelected() ? styles.nodeSelected : ''}`}
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
        <span class={styles.nodeName}>{props.node.displayName}</span>
        {renderNodeDiffStats(props.node, props.gitStatusStore)}
        <div class={styles.nodeActions}>
          {renderTreeContextMenu({
            path: props.node.path,
            isDir: props.node.isDir,
            rootPath: props.rootPath,
            homeDir: props.homeDir,
            onMention: props.onMention,
            onOpenTerminal: props.onOpenTerminal,
          })}
        </div>
      </div>
      <Show when={loading()}>
        <div class={styles.loadingInline} style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}>
          Loading...
        </div>
      </Show>
      <Show when={loaded()}>
        <div class={`${styles.childrenWrapper}${expanded() && !loading() ? ` ${styles.childrenWrapperExpanded}` : ''}`}>
          <div class={styles.childrenInner}>
            <For each={children()}>
              {child => (
                <TreeNode
                  node={child}
                  workerId={props.workerId}
                  showFiles={props.showFiles}
                  selectedPath={props.selectedPath}
                  onSelect={props.onSelect}
                  onFileOpen={props.onFileOpen}
                  onMention={props.onMention}
                  onOpenTerminal={props.onOpenTerminal}
                  depth={props.depth + 1}
                  scrollContainer={props.scrollContainer}
                  rootPath={props.rootPath}
                  homeDir={props.homeDir}
                  isNodeExpanded={props.isNodeExpanded}
                  setNodeExpanded={props.setNodeExpanded}
                  getChildren={props.getChildren}
                  setChildren={props.setChildren}
                  gitStatusStore={props.gitStatusStore}
                  visiblePaths={props.visiblePaths}
                />
              )}
            </For>
            <Show when={children().length === 0}>
              <div class={styles.emptyInline} style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}>
                Empty
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

  // -------------------------------------------------------------------------
  // Centralized tree state: expanded paths + children cache
  // -------------------------------------------------------------------------
  const [state, setState] = createStore<{
    expandedPaths: Record<string, boolean>
    childrenCache: Record<string, TreeNodeData[]>
  }>({
    expandedPaths: {},
    childrenCache: {},
  })

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
    })
  })

  const storageKey = () => `directoryTree:state:${props.rootPath ?? '~'}`

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
    })
  })

  // Persist state whenever it changes
  createEffect(() => {
    // Read both to subscribe
    const expanded = state.expandedPaths
    const cache = state.childrenCache
    try {
      sessionStorage.setItem(storageKey(), serializeState(expanded, cache))
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
  const setChildrenInStore = (path: string, data: TreeNodeData[]) => {
    setState(produce((s) => {
      s.childrenCache[path] = data
    }))
  }

  const rootPath = () => props.rootPath ?? '~'
  const rootDisplayName = () => {
    const rp = rootPath()
    return rp.split('/').pop() || rp
  }

  // Root children derived from the centralized cache, optionally filtered.
  const rootChildren = () => {
    const all = getChildren(rootPath())
    if (!all)
      return undefined
    const visible = props.visiblePaths
    if (!visible)
      return all
    return all.filter(c => visible.has(c.path))
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
      .then((children) => {
        if (version !== loadVersion)
          return
        setChildrenInStore(root, children)
        setLoading(false)
      })
      .catch((err) => {
        if (version !== loadVersion)
          return
        setError(err instanceof Error ? err.message : 'Failed to load directory')
        setLoading(false)
      })
  })

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
    if (value) {
      props.onSelect(value)
    }
  }

  return (
    <div class={styles.container}>
      <div class={styles.pathInput}>
        <input
          class={styles.pathInputField}
          type="text"
          value={inputValue()}
          title={props.selectedPath}
          onInput={e => setInputValue(e.currentTarget.value)}
          onKeyDown={handlePathKeyDown}
          onBlur={handlePathBlur}
          placeholder="Enter path..."
        />
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
              style={{ 'padding-left': '8px' }}
              data-testid="tree-root-node"
            >
              <Icon icon={FolderOpen} size="sm" class={styles.folderIcon} />
              <span class={styles.nodeName}>{rootDisplayName()}</span>
              <div class={styles.nodeActions}>
                {renderTreeContextMenu({
                  path: rootPath(),
                  isDir: true,
                  rootPath: rootPath(),
                  homeDir: props.homeDir,
                  onMention: props.onMention,
                  onOpenTerminal: props.onOpenTerminal,
                })}
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
                          workerId={props.workerId}
                          showFiles={props.showFiles ?? false}
                          selectedPath={props.selectedPath}
                          onSelect={props.onSelect}
                          onFileOpen={props.onFileOpen}
                          onMention={props.onMention}
                          onOpenTerminal={props.onOpenTerminal}
                          depth={0}
                          scrollContainer={treeRef}
                          rootPath={rootPath()}
                          homeDir={props.homeDir}
                          isNodeExpanded={isNodeExpanded}
                          setNodeExpanded={setNodeExpanded}
                          getChildren={getChildren}
                          setChildren={setChildrenInStore}
                          gitStatusStore={props.gitStatusStore}
                          visiblePaths={props.visiblePaths}
                        />
                      )}
                    </For>
                  </Show>
                </div>
              </div>
            </Show>
          </div>
        </Show>
      </div>
    </div>
  )
}
