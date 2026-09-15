import { createMemo, createSignal, onCleanup, onMount, Show } from 'solid-js'
import { DEFAULT_JSON_LINE_LENGTH, prettifyArgsJson, prettifyJson } from '~/lib/jsonFormat'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { CollapsibleText } from './CollapsibleText'
import * as styles from './ControlJson.css'

/** Format permission JSON for the available width with the shared Fractured JSON settings. */
export function ControlJson(props: { value: unknown, hideEmpty?: boolean, maxLines?: number }) {
  let root!: HTMLDivElement
  let probe!: HTMLSpanElement
  let body: HTMLElement | undefined
  const [columns, setColumns] = createSignal(DEFAULT_JSON_LINE_LENGTH)
  const text = createMemo(() => {
    try {
      return props.hideEmpty ? prettifyArgsJson(props.value, columns()) : prettifyJson(props.value, columns())
    }
    catch {
      return '{}'
    }
  })
  const measure = () => {
    if (!body)
      return
    const characterWidth = probe.getBoundingClientRect().width / 10
    const style = getComputedStyle(body)
    const width = body.clientWidth - (Number.parseFloat(style.paddingLeft) || 0) - (Number.parseFloat(style.paddingRight) || 0)
    if (width > 0 && characterWidth > 0)
      setColumns(Math.max(1, Math.min(DEFAULT_JSON_LINE_LENGTH, Math.floor(width / characterWidth) - 1)))
  }
  onMount(() => {
    measure()
    const observer = createRafResizeObserver(measure)
    observer?.observe(root)
    // Font changes alter the probe width, even when the panel width stays fixed.
    observer?.observe(probe)
    onCleanup(() => observer?.disconnect())
  })
  return (
    <div ref={root} class={styles.root} data-control-json={text() ? '' : undefined}>
      <span ref={probe} class={styles.probe} aria-hidden="true" />
      <Show when={text()}>
        <CollapsibleText text={text()} maxLines={props.maxLines ?? 6} class={styles.json} bodyRef={element => body = element} />
      </Show>
    </div>
  )
}
