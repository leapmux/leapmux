import type { Component, JSX } from 'solid-js'
import { createSignal, Show } from 'solid-js'
import * as styles from './ChatDropZone.css'

interface ChatDropZoneProps {
  children: JSX.Element
  onDrop?: (files: FileList, shiftKey: boolean) => void
  disabled?: boolean
}

export const ChatDropZone: Component<ChatDropZoneProps> = (props) => {
  const [isDragOver, setIsDragOver] = createSignal(false)
  let dragCounter = 0

  const hasFiles = (e: DragEvent) => e.dataTransfer?.types.includes('Files') ?? false

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
    if (dragCounter <= 0) {
      dragCounter = 0
      setIsDragOver(false)
    }
  }

  const handleDrop = (e: DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    dragCounter = 0
    setIsDragOver(false)
    if (props.disabled || !e.dataTransfer?.files.length)
      return
    props.onDrop?.(e.dataTransfer.files, e.shiftKey)
  }

  return (
    <div
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
