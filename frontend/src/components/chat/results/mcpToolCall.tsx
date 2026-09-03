import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { createMemo, For, Match, Show, Switch } from 'solid-js'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { parseImageBlock } from '~/lib/imageBlocks'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickString } from '~/lib/jsonPick'
import { renderMarkdownForContext } from '../messageRenderers'
import {
  toolInputSummary,
  toolMessage,
  toolResultContent,
  toolResultContentPre,
  toolResultError,
} from '../toolStyles.css'
import { ImageResultView } from './imageResult'

/** A single MCP content item produced by the server. */
export type McpContentItem
  = | { type: 'text', text: string }
    | { type: 'image', source: ImageResultSource }
    | { type: 'resource', uri: string, mimeType?: string }
    | { type: 'unknown', raw: unknown }

export type McpToolCallStatus = 'inProgress' | 'completed' | 'failed'

/**
 * Provider-neutral source for an MCP-style tool call (Claude `mcp__server__tool`,
 * Codex `mcpToolCall`, Codex `dynamicToolCall`). The body renders args + content
 * blocks + error; the caller wraps it in a header/layout per their convention.
 */
export interface McpToolCallSource {
  /** MCP server (or namespace) display name, e.g. `Tavily` / `siyuan`. */
  server: string
  /** Tool display name, e.g. `tavily_search`. */
  tool: string
  /** Pretty-JSON arguments for display. Empty when no args. */
  argsJson: string
  /** Result content blocks. Empty when there's no result yet (in-progress) or on error. */
  content: McpContentItem[]
  /** Pretty-JSON `structuredContent` (Codex). Undefined when the server didn't send one. */
  structuredJson?: string
  /** Error message when the call failed. */
  error?: string
  /** Tool-call status. */
  status: McpToolCallStatus
  /** Duration in milliseconds, when the agent reports it. */
  durationMs?: number
}

/** Display name fragment: "Server / tool" (or just "tool" when server is empty). */
export function mcpToolCallDisplayName(source: { server: string, tool: string }): string {
  return source.server ? `${source.server} / ${source.tool}` : source.tool
}

/**
 * Best-effort parse of one MCP/JSON-RPC content block into our discriminated
 * union. Recognizes the standard shapes (`text`, `image`, `resource`) and
 * keeps anything else as `unknown` for raw-JSON display.
 */
export function parseMcpContentItem(raw: unknown): McpContentItem {
  if (!isObject(raw))
    return { type: 'unknown', raw }
  const obj = raw
  const t = pickString(obj, 'type')
  if (t === 'text' && typeof obj.text === 'string')
    return { type: 'text', text: obj.text as string }
  // `parseImageBlock` also accepts the Anthropic `source:{...}` shape, which
  // Claude tool_result content blocks use. This parser read only the flat
  // `data`/`url` keys, so an Anthropic-shaped image rendered as `[image]`.
  const image = parseImageBlock(obj)
  if (image)
    return { type: 'image', source: image }
  if (t === 'resource' && typeof obj.uri === 'string') {
    return {
      type: 'resource',
      uri: obj.uri as string,
      mimeType: pickString(obj, 'mimeType', undefined),
    }
  }
  return { type: 'unknown', raw }
}

/**
 * Body for an MCP tool call: arguments (collapsible), content blocks, optional
 * structured payload, and any error. Does NOT render the server/tool header —
 * the caller owns that (typically via `ToolUseLayout` with
 * `mcpToolCallDisplayName` as the title).
 */
export function McpToolCallBody(props: {
  source: McpToolCallSource
  context?: RenderContext
}): JSX.Element {
  // Each image's position among the IMAGES of this message, which is what an
  // image tab addresses -- not its position among the content items, which
  // counts the text blocks between them. `Provider.toolResultImages` produces
  // the same ordering from the same blocks, so index N here and index N there
  // are the same picture.
  const imageOrdinals = createMemo(() => {
    let seen = 0
    return props.source.content.map(item => item.type === 'image' ? seen++ : -1)
  })
  return (
    <div class={toolMessage}>
      <Show when={props.source.argsJson}>
        <div class={toolInputSummary}>Arguments</div>
        <div class={toolResultContentPre}>{props.source.argsJson}</div>
      </Show>
      <Show when={props.source.content.length > 0}>
        <For each={props.source.content}>
          {(item, index) => (
            <McpContentItemView
              item={item}
              imageIndex={imageOrdinals()[index()]}
              title={mcpToolCallDisplayName(props.source)}
              context={props.context}
            />
          )}
        </For>
      </Show>
      <Show when={props.source.structuredJson}>
        <div class={toolInputSummary}>Structured</div>
        <div class={toolResultContentPre}>{props.source.structuredJson}</div>
      </Show>
      <Show when={props.source.error}>
        <div class={toolResultError}>{props.source.error}</div>
      </Show>
    </div>
  )
}

function McpContentItemView(props: { item: McpContentItem, imageIndex?: number, title?: string, context?: RenderContext }): JSX.Element {
  const markdownHtml = (text: string) => renderMarkdownForContext(text, props.context)
  return (
    <Switch>
      <Match when={props.item.type === 'text'}>
        <div
          class={toolResultContent}
          ref={cachedInnerHtml(() => markdownHtml((props.item as { type: 'text', text: string }).text))}
        />
      </Match>
      <Match when={props.item.type === 'image'}>
        <ImageResultView
          source={(props.item as { type: 'image', source: ImageResultSource }).source}
          index={props.imageIndex}
          title={props.title}
          context={props.context}
        />
      </Match>
      <Match when={props.item.type === 'resource'}>
        <McpResourceView item={props.item as { type: 'resource', uri: string, mimeType?: string }} />
      </Match>
      <Match when={props.item.type === 'unknown'}>
        <div class={toolResultContentPre}>
          {prettifyJson((props.item as { type: 'unknown', raw: unknown }).raw)}
        </div>
      </Match>
    </Switch>
  )
}

function McpResourceView(props: {
  item: { type: 'resource', uri: string, mimeType?: string }
}): JSX.Element {
  return (
    <div class={toolInputSummary}>
      [resource:
      {' '}
      {props.item.uri}
      {props.item.mimeType ? ` (${props.item.mimeType})` : ''}
      ]
    </div>
  )
}
