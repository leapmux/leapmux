import type { Component } from 'solid-js'
import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import File from 'lucide-solid/icons/file'
import Folder from 'lucide-solid/icons/folder'
import { createEffect, createSignal, For, Show } from 'solid-js'
import { fileClient } from '~/api/clients'
import * as styles from './DirectoryTree.css'

export interface DirectoryTreeProps {
  workerId: string
  showFiles?: boolean
  selectedPath: string
  onSelect: (path: string) => void
  rootPath?: string
}

interface TreeNodeData {
  path: string
  displayName: string
  isDir: boolean
  size: bigint
}

function sortEntries(a: FileInfo, b: FileInfo): number {
  if (a.isDir !== b.isDir)
    return a.isDir ? -1 : 1
  return a.name.localeCompare(b.name)
}

async function tryMerge(
  workerId: string,
  name: string,
  path: string,
  dirOnly: boolean,
  depth: number,
): Promise<TreeNodeData> {
  if (depth >= 5) {
    return { path, displayName: name, isDir: true, size: 0n }
  }
  try {
    const resp = await fileClient.listDirectory({ workerId, path })
    const children = dirOnly
      ? resp.entries.filter(e => e.isDir)
      : [...resp.entries]
    if (children.length === 1 && children[0].isDir) {
      return tryMerge(
        workerId,
        `${name}/${children[0].name}`,
        children[0].path,
        dirOnly,
        depth + 1,
      )
    }
  }
  catch {
    // Can't load, return unmerged
  }
  return { path, displayName: name, isDir: true, size: 0n }
}

async function loadChildren(
  workerId: string,
  dirPath: string,
  showFiles: boolean,
): Promise<TreeNodeData[]> {
  const resp = await fileClient.listDirectory({ workerId, path: dirPath })
  const entries = [...resp.entries].sort(sortEntries)
  const dirOnly = !showFiles
  const filtered = dirOnly ? entries.filter(e => e.isDir) : entries

  const result: TreeNodeData[] = []
  for (const entry of filtered) {
    if (entry.isDir) {
      const merged = await tryMerge(workerId, entry.name, entry.path, dirOnly, 0)
      result.push(merged)
    }
    else {
      result.push({
        path: entry.path,
        displayName: entry.name,
        isDir: false,
        size: entry.size,
      })
    }
  }
  return result
}

const TreeNode: Component<{
  node: TreeNodeData
  workerId: string
  showFiles: boolean
  selectedPath: string
  onSelect: (path: string) => void
  depth: number
}> = (props) => {
  const [expanded, setExpanded] = createSignal(false)
  const [children, setChildren] = createSignal<TreeNodeData[]>([])
  const [loading, setLoading] = createSignal(false)
  const [loaded, setLoaded] = createSignal(false)

  const isSelected = () => props.selectedPath === props.node.path

  const toggle = async () => {
    if (!props.node.isDir) {
      props.onSelect(props.node.path)
      return
    }
    props.onSelect(props.node.path)
    if (!loaded()) {
      setLoading(true)
      try {
        const result = await loadChildren(props.workerId, props.node.path, props.showFiles)
        setChildren(result)
        setLoaded(true)
      }
      catch {
        // ignore load errors
      }
      finally {
        setLoading(false)
      }
    }
    setExpanded(!expanded())
  }

  const indent = () => `${8 + props.depth * 16}px`

  return (
    <>
      <div
        class={`${styles.node} ${isSelected() ? styles.nodeSelected : ''}`}
        style={{ 'padding-left': indent() }}
        onClick={toggle}
      >
        <Show
          when={props.node.isDir}
          fallback={<span class={styles.chevronPlaceholder} />}
        >
          <Show
            when={expanded()}
            fallback={<ChevronRight size={16} class={styles.chevron} />}
          >
            <ChevronDown size={16} class={styles.chevron} />
          </Show>
        </Show>
        <Show
          when={props.node.isDir}
          fallback={<File size={14} class={styles.fileIcon} />}
        >
          <Folder size={14} class={styles.folderIcon} />
        </Show>
        <span class={styles.nodeName}>{props.node.displayName}</span>
      </div>
      <Show when={loading()}>
        <div class={styles.loadingInline} style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}>
          Loading...
        </div>
      </Show>
      <Show when={expanded() && !loading()}>
        <For each={children()}>
          {child => (
            <TreeNode
              node={child}
              workerId={props.workerId}
              showFiles={props.showFiles}
              selectedPath={props.selectedPath}
              onSelect={props.onSelect}
              depth={props.depth + 1}
            />
          )}
        </For>
        <Show when={children().length === 0 && loaded()}>
          <div class={styles.emptyInline} style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}>
            Empty
          </div>
        </Show>
      </Show>
    </>
  )
}

export const DirectoryTree: Component<DirectoryTreeProps> = (props) => {
  const [rootChildren, setRootChildren] = createSignal<TreeNodeData[]>([])
  const [loading, setLoading] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [inputValue, setInputValue] = createSignal('')
  let loadVersion = 0

  // Sync external selectedPath to input
  createEffect(() => {
    setInputValue(props.selectedPath)
  })

  // Load root children when workerId or rootPath changes
  createEffect(() => {
    const workerId = props.workerId
    const root = props.rootPath ?? '~'
    if (!workerId)
      return

    const version = ++loadVersion
    setLoading(true)
    setError(null)
    setRootChildren([])
    loadChildren(workerId, root, props.showFiles ?? false)
      .then((children) => {
        if (version !== loadVersion)
          return
        setRootChildren(children)
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
          onInput={e => setInputValue(e.currentTarget.value)}
          onKeyDown={handlePathKeyDown}
          onBlur={handlePathBlur}
          placeholder="Enter path..."
        />
      </div>
      <div class={styles.tree}>
        <Show when={error()}>
          <div class={styles.errorState}>{error()}</div>
        </Show>
        <Show when={loading()}>
          <div class={styles.loadingState}>Loading...</div>
        </Show>
        <Show when={!loading() && !error()}>
          <Show
            when={rootChildren().length > 0}
            fallback={<div class={styles.emptyState}>Empty directory</div>}
          >
            <For each={rootChildren()}>
              {node => (
                <TreeNode
                  node={node}
                  workerId={props.workerId}
                  showFiles={props.showFiles ?? false}
                  selectedPath={props.selectedPath}
                  onSelect={props.onSelect}
                  depth={0}
                />
              )}
            </For>
          </Show>
        </Show>
      </div>
    </div>
  )
}
