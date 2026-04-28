/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import { createMemo, For, Match, Show, Switch } from 'solid-js'
import { prettifyJson } from '~/lib/jsonFormat'
import { pickFirstString, pickString } from '~/lib/jsonPick'
import { renderMarkdown } from '~/lib/renderMarkdown'
import {
  mcpImage,
  mcpImageRow,
  toolInputSummary,
  toolMessage,
  toolResultContent,
  toolResultContentPre,
  toolResultError,
} from '../toolStyles.css'

/** A single MCP content item produced by the server. */
export type McpContentItem
  = | { type: 'text', text: string }
    | { type: 'image', mimeType?: string, urlOrData?: string }
    | { type: 'resource', uri: string, mimeType?: string }
    | { type: 'unknown', raw: unknown }

/**
 * MIME types we will render inline. SVG is intentionally excluded — SVGs can
 * carry script and we don't sandbox them.
 */
const RENDERABLE_IMAGE_MIME_TYPES = new Set<string>([
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
  'image/avif',
])

/**
 * Cap on the base64-encoded length of an inline image. Beyond this we
 * fall back to the placeholder rather than constructing a giant data URL.
 * 7 MB base64 ≈ 5 MB raw — comfortably covers screenshot-sized images.
 */
const MAX_INLINE_IMAGE_BASE64_LEN = 7 * 1024 * 1024

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
  if (raw === null || typeof raw !== 'object')
    return { type: 'unknown', raw }
  const obj = raw as Record<string, unknown>
  const t = pickString(obj, 'type')
  if (t === 'text' && typeof obj.text === 'string')
    return { type: 'text', text: obj.text as string }
  if (t === 'image') {
    return {
      type: 'image',
      mimeType: pickString(obj, 'mimeType', undefined),
      urlOrData: pickFirstString(obj, ['data', 'url']),
    }
  }
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
  return (
    <div class={toolMessage}>
      <Show when={props.source.argsJson}>
        <div class={toolInputSummary}>Arguments</div>
        <div class={toolResultContentPre}>{props.source.argsJson}</div>
      </Show>
      <Show when={props.source.content.length > 0}>
        <For each={props.source.content}>
          {item => <McpContentItemView item={item} />}
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

function McpContentItemView(props: { item: McpContentItem }): JSX.Element {
  return (
    <Switch>
      <Match when={props.item.type === 'text'}>
        <div
          class={toolResultContent}
          innerHTML={renderMarkdown((props.item as { type: 'text', text: string }).text)}
        />
      </Match>
      <Match when={props.item.type === 'image'}>
        <McpImageView item={props.item as { type: 'image', mimeType?: string, urlOrData?: string }} />
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

/**
 * Decision-tree for rendering an MCP image content block:
 *
 *   - Inline base64 + allowlisted MIME + under size cap → `<img src="data:...">`.
 *   - Already-formed `data:` URL with allowlisted MIME → render as-is.
 *   - http(s) URL → not rendered inline; the placeholder shows the URL as a
 *     link so the user can open it in a new tab.
 *   - Anything else → text placeholder so the user knows an image was
 *     returned but we're not rendering it (unknown MIME, bare base64
 *     without MIME, oversized inline data, etc.).
 */
export function imageRenderInfo(item: { type: 'image', mimeType?: string, urlOrData?: string }): {
  src?: string
  via?: 'inline'
  reason?: 'no-data' | 'unsupported-mime' | 'too-large' | 'external-url' | 'unknown-shape'
} {
  const data = item.urlOrData
  if (!data)
    return { reason: 'no-data' }

  // Already a complete data: URL — accept iff its MIME is allowlisted.
  const DATA_URL_PREFIX = 'data:'
  if (data.startsWith(DATA_URL_PREFIX)) {
    const semi = data.indexOf(';')
    const mime = semi > DATA_URL_PREFIX.length ? data.slice(DATA_URL_PREFIX.length, semi).toLowerCase() : ''
    if (!RENDERABLE_IMAGE_MIME_TYPES.has(mime))
      return { reason: 'unsupported-mime' }
    if (data.length > MAX_INLINE_IMAGE_BASE64_LEN)
      return { reason: 'too-large' }
    return { src: data, via: 'inline' }
  }

  // http(s) URL — show as an opt-in external link via the placeholder.
  if (data.startsWith('http://') || data.startsWith('https://'))
    return { reason: 'external-url' }

  // Plain base64 — only render when MIME is explicitly provided + allowlisted.
  const mime = (item.mimeType ?? '').toLowerCase()
  if (!RENDERABLE_IMAGE_MIME_TYPES.has(mime))
    return { reason: 'unsupported-mime' }
  if (data.length > MAX_INLINE_IMAGE_BASE64_LEN)
    return { reason: 'too-large' }
  return { src: `data:${mime};base64,${data}`, via: 'inline' }
}

function McpImageView(props: {
  item: { type: 'image', mimeType?: string, urlOrData?: string }
}): JSX.Element {
  const info = createMemo(() => imageRenderInfo(props.item))

  return (
    <Show
      when={info().src}
      fallback={<McpImagePlaceholder item={props.item} reason={info().reason} />}
    >
      <div class={mcpImageRow}>
        <img
          class={mcpImage}
          src={info().src}
          alt={props.item.mimeType ?? 'image'}
          loading="lazy"
          decoding="async"
          referrerpolicy="no-referrer"
        />
      </div>
    </Show>
  )
}

function McpImagePlaceholder(props: {
  item: { type: 'image', mimeType?: string, urlOrData?: string }
  reason?: 'no-data' | 'unsupported-mime' | 'too-large' | 'external-url' | 'unknown-shape'
}): JSX.Element {
  const looksLikeUrl = () => {
    const d = props.item.urlOrData ?? ''
    return d.startsWith('http://') || d.startsWith('https://')
  }
  const label = () => {
    const mime = props.item.mimeType
    const suffix = mime ? `: ${mime}` : ''
    if (props.reason === 'too-large')
      return `[image${suffix} — too large to render inline]`
    if (props.reason === 'unsupported-mime')
      return `[image${suffix} — unsupported format]`
    return `[image${suffix}]`
  }
  return (
    <div class={mcpImageRow}>
      <Show
        when={looksLikeUrl()}
        fallback={<div class={toolInputSummary}>{label()}</div>}
      >
        <div class={toolInputSummary}>
          {label()}
          {' — '}
          <a
            href={props.item.urlOrData}
            target="_blank"
            rel="noopener noreferrer nofollow"
            referrerpolicy="no-referrer"
          >
            open ↗
          </a>
        </div>
      </Show>
    </div>
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
