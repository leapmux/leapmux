import type { JSX } from 'solid-js'
import type { ViewMode } from './ViewToggle'
import { createMemo, createSignal, onCleanup, Show, untrack } from 'solid-js'
import { isSvgExtension } from '~/lib/fileType'
import * as styles from './FileViewer.css'
import { TextFileView } from './TextFileView'
import { ViewToggle } from './ViewToggle'

const MIME_MAP: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  bmp: 'image/bmp',
  webp: 'image/webp',
  svg: 'image/svg+xml',
  ico: 'image/x-icon',
  avif: 'image/avif',
}

function getMimeType(path: string): string {
  const ext = path.split('.').pop()?.toLowerCase() ?? ''
  return MIME_MAP[ext] ?? 'application/octet-stream'
}

function ImageRender(props: {
  content: Uint8Array
  filePath: string
}): JSX.Element {
  const blobUrl = createMemo(() => {
    const blob = new Blob([props.content], { type: getMimeType(props.filePath) })
    return URL.createObjectURL(blob)
  })

  onCleanup(() => {
    URL.revokeObjectURL(blobUrl())
  })

  return (
    <div class={styles.imageContainer}>
      <img
        src={blobUrl()}
        alt={props.filePath.split('/').pop() ?? 'Image'}
        class={styles.image}
      />
    </div>
  )
}

export function ImageFileView(props: {
  content: Uint8Array
  filePath: string
  totalSize?: number
  displayMode?: string
  onDisplayModeChange?: (mode: string) => void
}): JSX.Element {
  const isSvg = () => isSvgExtension(props.filePath)
  const [mode, setMode] = createSignal<ViewMode>(untrack(() => props.displayMode as ViewMode) || 'render')

  const handleModeChange = (m: ViewMode) => {
    setMode(m)
    props.onDisplayModeChange?.(m)
  }

  return (
    <Show
      when={isSvg()}
      fallback={<ImageRender content={props.content} filePath={props.filePath} />}
    >
      <div class={styles.toggleViewContainer}>
        <ViewToggle mode={mode()} onToggle={handleModeChange} />
        <Show
          when={mode() === 'render'}
          fallback={(
            <TextFileView
              content={props.content}
              filePath={props.filePath}
              totalSize={props.totalSize ?? props.content.length}
            />
          )}
        >
          <ImageRender content={props.content} filePath={props.filePath} />
        </Show>
      </div>
    </Show>
  )
}
