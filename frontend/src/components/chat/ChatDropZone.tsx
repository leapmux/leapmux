import type { Component, JSX } from 'solid-js'
import { createSignal, onCleanup, onMount, Show } from 'solid-js'
import * as styles from './ChatDropZone.css'

interface ChatDropZoneProps {
  children: JSX.Element
  onDrop?: (dataTransfer: DataTransfer, shiftKey: boolean) => void
  disabled?: boolean
}

export const ChatDropZone: Component<ChatDropZoneProps> = (props) => {
  const [isDragOver, setIsDragOver] = createSignal(false)
  let dropZoneRef: HTMLDivElement | undefined
  let dragCounter = 0

  const hasFiles = (e: DragEvent) => e.dataTransfer?.types.includes('Files') ?? false

  const resetDragState = () => {
    dragCounter = 0
    setIsDragOver(false)
  }

  const handleDragEnter = (e: DragEvent) => {
    if (props.disabled || !hasFiles(e))
      return
    e.preventDefault()
    dragCounter++
    if (dragCounter === 1)
      setIsDragOver(true)
  }

  const handleDragOver = (e: DragEvent) => {
    if (props.disabled || !hasFiles(e))
      return
    e.preventDefault()
    if (e.dataTransfer)
      e.dataTransfer.dropEffect = 'copy'
  }

  const handleDragLeave = () => {
    dragCounter--
    if (dragCounter <= 0)
      resetDragState()
  }

  const handleDrop = (e: DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    resetDragState()
    if (props.disabled || !e.dataTransfer?.files.length)
      return
    props.onDrop?.(e.dataTransfer, e.shiftKey)
  }

  // Always reset the overlay state when a drop lands anywhere inside the
  // drop zone — including when an inner handler (e.g. MarkdownEditor's
  // file-drop interceptor) calls stopPropagation() to keep the event out
  // of ProseMirror. A capture-phase listener on the outer element fires
  // before any inner capture-phase listener, so it runs even if the bubble
  // path is later blocked.
  onMount(() => {
    const el = dropZoneRef
    if (!el)
      return
    el.addEventListener('drop', resetDragState, true)
    onCleanup(() => el.removeEventListener('drop', resetDragState, true))
  })

  return (
    <div
      ref={dropZoneRef}
      class={styles.dropZone}
      onDragEnter={handleDragEnter}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      {props.children}
      <Show when={isDragOver()}>
        <div class={styles.dropOverlay}>
          <div class={styles.dropOverlayContent}>
            <span class={styles.dropOverlayTitle}>Drop files to attach</span>
            <span class={styles.dropOverlayHint}>Hold Shift to send immediately</span>
          </div>
        </div>
      </Show>
    </div>
  )
}
