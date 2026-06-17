import type { Component } from 'solid-js'
import { Index, Show } from 'solid-js'
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

/** Resolve the CSS class(es) for a single span-line column (null = empty). */
function classFor(line: SpanLine | null): string {
  if (line === null)
    return spanLineEmpty
  const baseClass = TYPE_STYLES[line.type] || spanLineActive
  const colorClass = line.color > 0
    ? spanLineColors[spanColorKey(line.color)]
    : ''
  const ptClass = line.passthrough_color != null && line.passthrough_color > 0
    ? spanPassthroughColors[spanColorKey(line.passthrough_color)]
    : ''
  return `${baseClass} ${colorClass} ${ptClass}`
}

export const SpanLines: Component<SpanLinesProps> = (props) => {
  return (
    <Show when={props.lines && props.lines.length > 0}>
      <div class={props.spanOpener ? spanLinesContainerSpanOpener : spanLinesContainer}>
        {/*
          <Index> keys columns by position (the array is positional plain data),
          so re-parsing span_lines updates each column's class in place instead
          of recreating every column div the way reference-keyed <For> would.
        */}
        <Index each={props.lines}>
          {line => <div class={classFor(line())} />}
        </Index>
      </div>
    </Show>
  )
}
