import type { Component } from 'solid-js'
import { For, Show } from 'solid-js'
import {
  PALETTE_SIZE,
  spanLineActive,
  spanLineColors,
  spanLineConnector,
  spanLineEmpty,
  spanLinesContainer,
  spanLinesContainerSpanOpener,
} from './SpanLines.css'

export interface SpanLine {
  span_id: string
  color: number
}

interface SpanLinesProps {
  lines: (SpanLine | null)[]
  parentSpanId: string
  spanOpener?: boolean
}

export const SpanLines: Component<SpanLinesProps> = (props) => {
  return (
    <Show when={props.lines && props.lines.length > 0}>
      <div class={props.spanOpener ? spanLinesContainerSpanOpener : spanLinesContainer}>
        <For each={props.lines}>
          {(line) => {
            if (line === null)
              return <div class={spanLineEmpty} />

            const colorClass = spanLineColors[`color${line.color % PALETTE_SIZE}`]
            const isConnected = line.span_id === props.parentSpanId

            if (isConnected)
              return <div class={`${spanLineConnector} ${colorClass}`} />

            return <div class={`${spanLineActive} ${colorClass}`} />
          }}
        </For>
      </div>
    </Show>
  )
}
