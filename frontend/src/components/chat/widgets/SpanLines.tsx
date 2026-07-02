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

/** The palette class that sets --span-line-color for a 1-based color index (or '' for none). */
export function spanColorClassFor(color: number | undefined): string {
  return color !== undefined && color > 0 ? spanLineColors[spanColorKey(color)] : ''
}

export interface SpanLine {
  span_id?: string
  color?: number
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
  const colorClass = spanColorClassFor(line.color)
  const ptClass = line.passthrough_color != null && line.passthrough_color > 0
    ? spanPassthroughColors[spanColorKey(line.passthrough_color)]
    : ''
  return `${baseClass} ${colorClass} ${ptClass}`
}

function hasVerticalTop(line: SpanLine | null): line is SpanLine {
  return line !== null && (
    line.type === 'active'
    || line.type === 'connector'
    || line.type === 'connector_end'
    || line.type === 'active_passthrough'
  )
}

function hasVerticalBottom(line: SpanLine | null): line is SpanLine {
  return line !== null && (
    line.type === 'active'
    || line.type === 'connector'
    || line.type === 'active_passthrough'
  )
}

function verticalSpanKey(line: SpanLine): string | undefined {
  if (line.span_id)
    return `id:${line.span_id}`
  const color = line.color ?? 0
  return color > 0 ? `color:${color}` : undefined
}

export function bodySpanKey(spanId: string | undefined, spanColor: number | undefined): string | undefined {
  if (spanId)
    return `id:${spanId}`
  return spanColor !== undefined && spanColor > 0 ? `color:${spanColor}` : undefined
}

/**
 * Whether a column's vertical rail continues from the row above — i.e. the
 * inter-row gap needs a bridge segment for this column. Consumed by the
 * gap-bridge overlay (SpanLineGapBridges); the in-row columns themselves
 * stay strictly inside the row box so rows can paint-contain.
 */
export function shouldConnectSpanLineTop(line: SpanLine | null, previousLine: SpanLine | null, previousBodySpanKey?: string): boolean {
  if (!hasVerticalTop(line))
    return false
  const lineKey = verticalSpanKey(line)
  if (lineKey === undefined)
    return false
  if (lineKey === previousBodySpanKey)
    return true
  if (!hasVerticalBottom(previousLine))
    return false
  return lineKey === verticalSpanKey(previousLine)
}

export const SpanLines: Component<SpanLinesProps> = (props) => {
  const containerClass = () => props.spanOpener ? spanLinesContainerSpanOpener : spanLinesContainer

  return (
    <Show when={props.lines && props.lines.length > 0}>
      <div class={containerClass()}>
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
