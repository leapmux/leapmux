import type { JSX } from 'solid-js'
import type { ZoomMode } from './ImageToolbar'
import type { ViewMode } from './ViewToggle'
import AtSign from 'lucide-solid/icons/at-sign'
import { createEffect, createMemo, createSignal, Match, onCleanup, Show, Switch, untrack } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { isSvgExtension } from '~/lib/fileType'
import * as styles from './FileViewer.css'
import { ImageToolbar, ZOOM_MAX, ZOOM_MIN } from './ImageToolbar'
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

const WRAPPER_PADDING = 16 // matches var(--space-4) used in imageZoomWrapper

function getMimeType(path: string): string {
  const ext = path.split('.').pop()?.toLowerCase() ?? ''
  return MIME_MAP[ext] ?? 'application/octet-stream'
}

function ImageRender(props: {
  content: Uint8Array
  filePath: string
  zoom: ZoomMode
  onZoomChange: (mode: ZoomMode) => void
}): JSX.Element {
  let containerRef!: HTMLDivElement
  let scrollRef!: HTMLDivElement
  let imgRef!: HTMLImageElement
  const [naturalSize, setNaturalSize] = createSignal<{ w: number, h: number } | null>(null)
  const [containerSize, setContainerSize] = createSignal({ w: 0, h: 0 })

  const blobUrl = createMemo(() => {
    const blob = new Blob([props.content], { type: getMimeType(props.filePath) })
    return URL.createObjectURL(blob)
  })

  onCleanup(() => {
    URL.revokeObjectURL(blobUrl())
  })

  createEffect(() => {
    const observer = new ResizeObserver(([entry]) => {
      if (entry)
        setContainerSize({ w: entry.contentRect.width, h: entry.contentRect.height })
    })
    observer.observe(containerRef)
    onCleanup(() => observer.disconnect())
  })

  const fitScale = createMemo(() => {
    const ns = naturalSize()
    const cs = containerSize()
    if (!ns?.w || !ns?.h || !cs.w || !cs.h)
      return null
    const availW = cs.w - WRAPPER_PADDING * 2
    const availH = cs.h - WRAPPER_PADDING * 2
    if (availW <= 0 || availH <= 0)
      return null
    return Math.min(availW / ns.w, availH / ns.h)
  })

  // Trackpad pinch-to-zoom: ctrlKey+wheel events zoom toward cursor position
  createEffect(() => {
    const el = scrollRef
    if (!el)
      return

    const handleWheel = (e: WheelEvent) => {
      if (!e.ctrlKey)
        return
      e.preventDefault()

      const ns = naturalSize()
      if (!ns)
        return

      const currentZoom = props.zoom
      const oldScale = currentZoom === 'fit'
        ? (fitScale() ?? 1)
        : currentZoom === 'actual' ? 1 : currentZoom

      const newScale = Math.min(
        ZOOM_MAX,
        Math.max(ZOOM_MIN, oldScale * (1 + -e.deltaY * 0.01)),
      )

      // Cursor position as a fraction of the image
      const imgRect = imgRef.getBoundingClientRect()
      const fracX = (e.clientX - imgRect.left) / imgRect.width
      const fracY = (e.clientY - imgRect.top) / imgRect.height

      // Cursor position within the scroll viewport
      const scrollRect = el.getBoundingClientRect()
      const viewX = e.clientX - scrollRect.left
      const viewY = e.clientY - scrollRect.top

      // SolidJS updates DOM synchronously
      props.onZoomChange(newScale)

      // Compute new scroll position to keep the point under cursor stable
      const newImgW = ns.w * newScale
      const newImgH = ns.h * newScale
      const contentW = Math.max(el.clientWidth, newImgW + WRAPPER_PADDING * 2)
      const contentH = Math.max(el.clientHeight, newImgH + WRAPPER_PADDING * 2)
      const imgOffsetX = (contentW - newImgW) / 2
      const imgOffsetY = (contentH - newImgH) / 2
      el.scrollLeft = imgOffsetX + fracX * newImgW - viewX + e.deltaX
      el.scrollTop = imgOffsetY + fracY * newImgH - viewY
    }

    el.addEventListener('wheel', handleWheel, { passive: false })
    onCleanup(() => el.removeEventListener('wheel', handleWheel))
  })

  const displaySize = createMemo(() => {
    const ns = naturalSize()
    if (!ns)
      return null
    const z = props.zoom
    if (z === 'fit') {
      const fs = fitScale()
      if (fs == null)
        return null
      return { w: ns.w * fs, h: ns.h * fs }
    }
    const s = z === 'actual' ? 1 : z
    return { w: ns.w * s, h: ns.h * s }
  })

  const alt = () => props.filePath.split('/').pop() ?? 'Image'

  const handleLoad: JSX.EventHandler<HTMLImageElement, Event> = (e) => {
    const img = e.currentTarget
    setNaturalSize({ w: img.naturalWidth, h: img.naturalHeight })
  }

  return (
    <div ref={containerRef} class={styles.imageRenderContainer}>
      <ImageToolbar zoom={props.zoom} fitScale={fitScale()} onZoomChange={props.onZoomChange} />
      <div ref={scrollRef} class={styles.imageScrollContainer}>
        <div class={styles.imageZoomWrapper}>
          <img
            ref={imgRef}
            src={blobUrl()}
            alt={alt()}
            class={styles.imageCheckerboard}
            style={displaySize()
              ? {
                  width: `${displaySize()!.w}px`,
                  height: `${displaySize()!.h}px`,
                }
              : {
                  'max-width': '100%',
                  'max-height': '100%',
                  'object-fit': 'contain',
                }}
            onLoad={handleLoad}
          />
        </div>
      </div>
    </div>
  )
}

export function ImageFileView(props: {
  content: Uint8Array
  filePath: string
  totalSize?: number
  displayMode?: string
  onDisplayModeChange?: (mode: string) => void
  onQuote?: (text: string, startLine?: number, endLine?: number) => void
  onMention?: () => void
}): JSX.Element {
  const isSvg = () => isSvgExtension(props.filePath)
  const [mode, setMode] = createSignal<ViewMode>(untrack(() => props.displayMode as ViewMode) || 'render')
  const [zoom, setZoom] = createSignal<ZoomMode>('fit')

  const handleModeChange = (m: ViewMode) => {
    setMode(m)
    props.onDisplayModeChange?.(m)
  }

  const renderImage = () => (
    <ImageRender
      content={props.content}
      filePath={props.filePath}
      zoom={zoom()}
      onZoomChange={setZoom}
    />
  )

  return (
    <Switch>
      <Match when={!isSvg()}>
        <div style={{ position: 'relative', height: '100%' }}>
          <Show when={props.onMention}>
            <div class={styles.viewToggle}>
              <button
                class={styles.viewToggleButton}
                onClick={() => props.onMention?.()}
                title="Mention in the chat"
                data-testid="file-mention-button"
              >
                <Icon icon={AtSign} size="sm" />
              </button>
            </div>
          </Show>
          {renderImage()}
        </div>
      </Match>
      <Match when={isSvg()}>
        <div class={styles.toggleViewContainer}>
          <ViewToggle mode={mode()} onToggle={handleModeChange} showSplit onMention={props.onMention} />
          <Switch>
            <Match when={mode() === 'render'}>
              {renderImage()}
            </Match>
            <Match when={mode() === 'split'}>
              <div class={styles.splitContainer}>
                <div class={styles.splitPane}>
                  {renderImage()}
                </div>
                <div class={styles.splitDivider} />
                <div class={styles.splitPane}>
                  <TextFileView
                    content={props.content}
                    filePath={props.filePath}
                    totalSize={props.totalSize ?? props.content.length}
                    onQuote={props.onQuote}
                  />
                </div>
              </div>
            </Match>
            <Match when={mode() === 'source'}>
              <TextFileView
                content={props.content}
                filePath={props.filePath}
                totalSize={props.totalSize ?? props.content.length}
                onQuote={props.onQuote}
              />
            </Match>
          </Switch>
        </div>
      </Match>
    </Switch>
  )
}
