import type { Component } from 'solid-js'
import { For, Show } from 'solid-js'
import {
  PALETTE_SIZE,
  spanLineActive,
  spanLineActivePassthrough,
  spanLineColors,
  spanLineConnector,
  spanLineConnectorEnd,
  spanLineEmpty,
  spanLinePassthrough,
  spanLinesContainer,
  spanLinesContainerSpanOpener,
  spanPassthroughColors,
} from './SpanLines.css'

/** Map a 1-based server color index to the corresponding CSS class key. */
export function spanColorKey(colorIndex: number): string {
  return `color${(colorIndex - 1) % PALETTE_SIZE}`
}

export interface SpanLine {
  span_id: string
  color: number
  type: 'active' | 'connector' | 'connector_end' | 'passthrough' | 'active_passthrough'
  passthrough_color?: number
}

interface SpanLinesProps {
  lines: (SpanLine | null)[]
  spanOpener?: boolean
}

const TYPE_STYLES: Record<SpanLine['type'], string> = {
  active: spanLineActive,
  connector: spanLineConnector,
  connector_end: spanLineConnectorEnd,
  passthrough: spanLinePassthrough,
  active_passthrough: spanLineActivePassthrough,
}

export const SpanLines: Component<SpanLinesProps> = (props) => {
  return (
    <Show when={props.lines && props.lines.length > 0}>
      <div class={props.spanOpener ? spanLinesContainerSpanOpener : spanLinesContainer}>
        <For each={props.lines}>
          {(line) => {
            if (line === null)
              return <div class={spanLineEmpty} />

            const baseClass = TYPE_STYLES[line.type] || spanLineActive
            const colorClass = line.color > 0
              ? spanLineColors[spanColorKey(line.color)]
              : ''
            const ptClass = line.passthrough_color != null && line.passthrough_color > 0
              ? spanPassthroughColors[spanColorKey(line.passthrough_color)]
              : ''

            return <div class={`${baseClass} ${colorClass} ${ptClass}`} />
          }}
        </For>
      </div>
    </Show>
  )
}
