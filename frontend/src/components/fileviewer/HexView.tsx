import type { JSX } from 'solid-js'
import { createEffect, createMemo, createSignal, For, on, onCleanup, onMount } from 'solid-js'
import * as styles from './FileViewer.css'

const FALLBACK_ROW_HEIGHT = 20
const BASE_VPAD = 4 // matches var(--space-1)
const OVERSCAN = 20

/** Format a single hex row: offset | hex bytes | ASCII */
function formatRow(bytes: Uint8Array, offset: number): { offsetStr: string, hex: string, ascii: string } {
  const offsetStr = offset.toString(16).padStart(8, '0')
  const hexParts: string[] = []
  const asciiParts: string[] = []

  for (let i = 0; i < 16; i++) {
    if (i === 8)
      hexParts.push('')
    if (i < bytes.length) {
      hexParts.push(bytes[i].toString(16).padStart(2, '0'))
      const b = bytes[i]
      asciiParts.push(b >= 0x20 && b <= 0x7E ? String.fromCharCode(b) : '.')
    }
    else {
      hexParts.push('  ')
      asciiParts.push(' ')
    }
  }

  return {
    offsetStr,
    hex: hexParts.join(' '),
    ascii: asciiParts.join(''),
  }
}

export function HexView(props: {
  content: Uint8Array
  totalSize: number
}): JSX.Element {
  let scrollRef!: HTMLDivElement
  const [scrollTop, setScrollTop] = createSignal(0)
  const [viewHeight, setViewHeight] = createSignal(400)
  const [rowHeight, setRowHeight] = createSignal(FALLBACK_ROW_HEIGHT)

  const totalRows = createMemo(() => Math.max(1, Math.ceil(props.content.length / 16)))

  const startIdx = createMemo(() =>
    Math.max(0, Math.floor(scrollTop() / rowHeight()) - OVERSCAN),
  )

  const endIdx = createMemo(() =>
    Math.min(totalRows(), Math.ceil((scrollTop() + viewHeight()) / rowHeight()) + OVERSCAN),
  )

  const visibleRows = createMemo(() => {
    const s = startIdx()
    const e = endIdx()
    const result: Array<{ offset: number, bytes: Uint8Array }> = []
    for (let i = s; i < e; i++) {
      const off = i * 16
      result.push({
        offset: off,
        bytes: props.content.subarray(off, Math.min(off + 16, props.content.length)),
      })
    }
    return result
  })

  // Reset scroll when content changes
  createEffect(on(() => props.content, () => {
    if (scrollRef)
      scrollRef.scrollTop = 0
    setScrollTop(0)
  }))

  onMount(() => {
    const obs = new ResizeObserver(([entry]) => setViewHeight(entry.contentRect.height))
    obs.observe(scrollRef)
    onCleanup(() => obs.disconnect())

    // Measure actual row height from a rendered row
    requestAnimationFrame(() => {
      const row = scrollRef.querySelector('[data-hex-row]') as HTMLElement
      if (row)
        setRowHeight(row.offsetHeight)
    })
  })

  const topPad = () => `${BASE_VPAD + startIdx() * rowHeight()}px`
  const bottomPad = () => `${BASE_VPAD + Math.max(0, totalRows() - endIdx()) * rowHeight()}px`

  return (
    <div
      ref={scrollRef}
      class={styles.hexScroll}
      onScroll={() => setScrollTop(scrollRef.scrollTop)}
    >
      <div
        class={styles.hexContainer}
        style={{ 'padding-top': topPad(), 'padding-bottom': bottomPad() }}
      >
        <For each={visibleRows()}>
          {(row) => {
            const formatted = formatRow(row.bytes, row.offset)
            return (
              <div data-hex-row>
                <span class={styles.hexOffset}>{formatted.offsetStr}</span>
                <span class={styles.hexSeparator}>{'  '}</span>
                <span>{formatted.hex}</span>
                <span class={styles.hexSeparator}>{'  '}</span>
                <span class={styles.hexAscii}>{`\u2502${formatted.ascii}\u2502`}</span>
              </div>
            )
          }}
        </For>
      </div>
    </div>
  )
}

/** Export the row formatter for testing. */
export { formatRow }
