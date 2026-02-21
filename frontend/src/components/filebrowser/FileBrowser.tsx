import type { Component } from 'solid-js'
import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import { For, Show } from 'solid-js'
import * as styles from './FileBrowser.css'

interface FileBrowserProps {
  currentPath: string
  entries: FileInfo[]
  loading: boolean
  error: string | null
  onNavigate: (path: string) => void
  onFileSelect: (entry: FileInfo) => void
}

function formatSize(bytes: bigint): string {
  const n = Number(bytes)
  if (n < 1024)
    return `${n} B`
  if (n < 1024 * 1024)
    return `${(n / 1024).toFixed(1)} K`
  if (n < 1024 * 1024 * 1024)
    return `${(n / (1024 * 1024)).toFixed(1)} M`
  return `${(n / (1024 * 1024 * 1024)).toFixed(1)} G`
}

function pathSegments(path: string): { name: string, path: string }[] {
  const parts = path.split('/').filter(Boolean)
  const segments: { name: string, path: string }[] = []
  for (let i = 0; i < parts.length; i++) {
    segments.push({
      name: parts[i],
      path: `/${parts.slice(0, i + 1).join('/')}`,
    })
  }
  return segments
}

function sortedEntries(entries: FileInfo[]): FileInfo[] {
  return [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir)
      return a.isDir ? -1 : 1
    return a.name.localeCompare(b.name)
  })
}

export const FileBrowser: Component<FileBrowserProps> = (props) => {
  const handleClick = (entry: FileInfo) => {
    if (entry.isDir) {
      props.onNavigate(entry.path)
    }
    else {
      props.onFileSelect(entry)
    }
  }

  return (
    <div class={styles.container}>
      <nav aria-label="Breadcrumb" class={styles.pathBar}>
        <ol class={styles.breadcrumbList}>
          <li>
            <button class={styles.pathSegment} onClick={() => props.onNavigate('.')}>
              ~
            </button>
          </li>
          <For each={pathSegments(props.currentPath)}>
            {segment => (
              <li>
                <span class={styles.pathSeparator}>/</span>
                <button
                  class={styles.pathSegment}
                  onClick={() => props.onNavigate(segment.path)}
                >
                  {segment.name}
                </button>
              </li>
            )}
          </For>
        </ol>
      </nav>

      <Show when={props.loading}>
        <div class={styles.loadingState}>Loading...</div>
      </Show>

      <Show when={props.error}>
        <div class={styles.errorState}>{props.error}</div>
      </Show>

      <Show when={!props.loading && !props.error}>
        <Show
          when={props.entries.length > 0}
          fallback={<div class={styles.emptyState}>Empty directory</div>}
        >
          <div class={styles.fileList}>
            <Show when={props.currentPath !== '.'}>
              <div
                class={styles.fileItem}
                onClick={() => {
                  const parts = props.currentPath.split('/')
                  parts.pop()
                  props.onNavigate(parts.join('/') || '.')
                }}
              >
                <span class={styles.dirIcon}>..</span>
                <span class={styles.fileName}>..</span>
              </div>
            </Show>
            <For each={sortedEntries(props.entries)}>
              {entry => (
                <div
                  class={styles.fileItem}
                  onClick={() => handleClick(entry)}
                >
                  <span class={entry.isDir ? styles.dirIcon : styles.fileIcon}>
                    {entry.isDir ? 'D' : 'F'}
                  </span>
                  <span class={styles.fileName}>{entry.name}</span>
                  <Show when={!entry.isDir}>
                    <span class={styles.fileSize}>{formatSize(entry.size)}</span>
                  </Show>
                </div>
              )}
            </For>
          </div>
        </Show>
      </Show>
    </div>
  )
}
