import type { Component } from 'solid-js'
import { For, Show } from 'solid-js'
import {
  PALETTE_SIZE,
  threadLineActive,
  threadLineColors,
  threadLineConnector,
  threadLineEmpty,
  threadLinesContainer,
} from './ThreadLines.css'

export interface ThreadLine {
  scope_id: string
  color: number
}

interface ThreadLinesProps {
  lines: (ThreadLine | null)[]
  scopeId: string
}

export const ThreadLines: Component<ThreadLinesProps> = (props) => {
  return (
    <Show when={props.lines && props.lines.length > 0}>
      <div class={threadLinesContainer}>
        <For each={props.lines}>
          {(line) => {
            if (line === null)
              return <div class={threadLineEmpty} />

            const colorClass = threadLineColors[`color${line.color % PALETTE_SIZE}`]
            const isConnected = line.scope_id === props.scopeId

            if (isConnected)
              return <div class={`${threadLineConnector} ${colorClass}`} />

            return <div class={`${threadLineActive} ${colorClass}`} />
          }}
        </For>
      </div>
    </Show>
  )
}
