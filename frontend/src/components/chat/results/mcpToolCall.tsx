import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { createMemo, For, Match, Show, Switch } from 'solid-js'
import { parseImageBlock } from '~/lib/imageBlocks'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { getToolResultExpanded } from '../messageRenderers'
import {
  toolInputSummary,
  toolMessage,
  toolResultError,
  toolResultPrompt,
} from '../toolStyles.css'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './collapse'
import { CollapsibleContent } from './CollapsibleContent'
import { ImageResultView } from './imageResult'
import { useCollapsedLines } from './useCollapsedLines'

/** A single MCP content item produced by the server. */
export type McpContentItem
  = | { type: 'text', text: string }
    | { type: 'image', source: ImageResultSource }
    | { type: 'resource', uri: string, mimeType?: string, text?: string }
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
  const resource = t === 'resource' ? pickObject(obj, 'resource') ?? obj : undefined
  if (resource && typeof resource.uri === 'string' && !('blob' in resource)) {
    return {
      type: 'resource',
      uri: resource.uri,
      mimeType: pickString(resource, 'mimeType', undefined),
      ...(typeof resource.text === 'string' ? { text: resource.text } : {}),
    }
  }
  return { type: 'unknown', raw }
}

function mcpContentText(item: McpContentItem): string {
  switch (item.type) {
    case 'text': return item.text
    case 'resource': return [item.uri, item.text].filter(value => value !== undefined).join('\n')
    case 'unknown': return prettifyJson(item.raw)
    case 'image': return item.source.description ?? ''
  }
}

export function mcpToolCallCopyable(source: McpToolCallSource): string {
  return [...source.content.map(mcpContentText), source.structuredJson, source.error].filter(Boolean).join('\n\n')
}

export function mcpToolCallCollapsible(source: McpToolCallSource): boolean {
  return [source.argsJson, source.structuredJson, source.error, ...source.content.map(item => item.type === 'image' ? undefined : item.type === 'resource' ? item.text : mcpContentText(item))]
    .some(text => text !== undefined && hasMoreLinesThan(text, COLLAPSED_RESULT_ROWS))
}

export function mcpToolResultMeta(source: McpToolCallSource) {
  const text = mcpToolCallCopyable(source)
  return {
    collapsible: mcpToolCallCollapsible(source),
    hasDiff: false,
    hasCopyable: text !== '',
    copyableContent: () => text || null,
  }
}

function McpTextView(props: { text: string, markdown?: boolean, expanded: () => boolean, context?: RenderContext }): JSX.Element {
  const collapsed = useCollapsedLines({ text: () => props.text, expanded: () => props.expanded() })
  return <CollapsibleContent kind={props.markdown ? 'markdown-tool-result' : 'pre'} text={props.text} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} context={props.context} />
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
  expanded?: () => boolean
}): JSX.Element {
  const expanded = () => props.expanded?.() ?? getToolResultExpanded(props.context)
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
        <McpTextView text={props.source.argsJson} expanded={expanded} context={props.context} />
      </Show>
      <Show when={props.source.content.length > 0}>
        <For each={props.source.content}>
          {(item, index) => (
            <McpContentItemView
              item={item}
              imageIndex={imageOrdinals()[index()]}
              title={mcpToolCallDisplayName(props.source)}
              failed={props.source.status === 'failed'}
              context={props.context}
              expanded={expanded}
            />
          )}
        </For>
      </Show>
      <Show when={props.source.structuredJson}>
        <div class={toolInputSummary}>Structured</div>
        <McpTextView text={props.source.structuredJson!} expanded={expanded} context={props.context} />
      </Show>
      <Show when={props.source.error}>
        <div class={toolResultError}><McpTextView text={props.source.error!} expanded={expanded} context={props.context} /></div>
      </Show>
      <Show when={props.source.status !== 'inProgress' && props.source.content.length === 0 && !props.source.structuredJson && !props.source.error}>
        <div class={toolResultPrompt}>[no output]</div>
      </Show>
    </div>
  )
}

function McpContentItemView(props: { item: McpContentItem, imageIndex?: number, title?: string, failed?: boolean, context?: RenderContext, expanded: () => boolean }): JSX.Element {
  return (
    <Switch>
      <Match when={props.item.type === 'text'}>
        <McpTextView text={(props.item as { type: 'text', text: string }).text} markdown={!props.failed} expanded={props.expanded} context={props.context} />
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
        <McpResourceView item={props.item as Extract<McpContentItem, { type: 'resource' }>} expanded={props.expanded} context={props.context} />
      </Match>
      <Match when={props.item.type === 'unknown'}>
        <McpTextView text={prettifyJson((props.item as { type: 'unknown', raw: unknown }).raw)} expanded={props.expanded} context={props.context} />
      </Match>
    </Switch>
  )
}

function McpResourceView(props: {
  item: Extract<McpContentItem, { type: 'resource' }>
  expanded: () => boolean
  context?: RenderContext
}): JSX.Element {
  return (
    <>
      <div class={toolInputSummary}>
        [resource:
        {' '}
        {props.item.uri}
        {props.item.mimeType ? ` (${props.item.mimeType})` : ''}
        ]
      </div>
      <Show when={props.item.text !== undefined}>
        <McpTextView text={props.item.text!} expanded={props.expanded} context={props.context} />
      </Show>
    </>
  )
}
