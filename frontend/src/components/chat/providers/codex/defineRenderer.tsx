import type { Component, JSX } from 'solid-js'
import type { RenderContext } from '../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { CodexItemType } from '~/types/toolMessages'
import { Show } from 'solid-js'
import { extractItem } from './renderHelpers'

/**
 * Renderer for an already-unwrapped Codex item. Reads `props.item` reactively
 * — accessor reads inside `createMemo` only re-run when `item` actually
 * changes, so MessageBubble re-renders triggered by UI state (e.g. expand
 * toggle) skip recomputation of expensive per-item derivations.
 */
export interface CodexItemRendererProps {
  item: Record<string, unknown>
  role: MessageRole
  context?: RenderContext
}

export type CodexItemRenderer = Component<CodexItemRendererProps>

/**
 * Public renderer signature: takes the raw parsed message and unwraps via
 * `extractItem`. Same memoization story as `CodexItemRenderer` — only
 * `props.parsed`-derived work re-runs when the parsed message changes.
 */
export interface CodexMessageRendererProps {
  parsed: unknown
  role: MessageRole
  context?: RenderContext
}

export type CodexMessageRenderer = Component<CodexMessageRendererProps>

export interface CodexRendererSpec {
  /**
   * `item.type` values this renderer handles. Most renderers handle one type
   * (e.g. `CODEX_ITEM.COMMAND_EXECUTION`); MCP handles two
   * (`CODEX_ITEM.MCP_TOOL_CALL` and `CODEX_ITEM.DYNAMIC_TOOL_CALL`).
   */
  itemTypes: readonly CodexItemType[]
  /** The actual rendering logic — receives the unwrapped item via props. */
  render: CodexItemRenderer
}

/**
 * Registry mapping `item.type` → renderer component. Populated as a side
 * effect of importing each renderer file (each call to `defineCodexRenderer`
 * registers itself). The codex plugin's `renderMessage` looks up the right
 * renderer by item type and emits `<Renderer item={...} role={...}
 * context={...} />`.
 *
 * Note: `turnPlan` is *not* in this registry — its dispatch shape is
 * `parent.method === 'turn/plan/updated'` rather than `item.type`, so its
 * renderer remains a stand-alone component called explicitly by the plugin.
 */
// Map keyed by raw `string` rather than `CodexItemType` so the dispatch site
// can look up by `item.type as string` without casting. The `defineCodexRenderer`
// caller is the typed write side: only `CodexItemType` values can be inserted.
export const CODEX_RENDERERS: Map<string, CodexItemRenderer> = new Map()

/**
 * Build a Codex message renderer with the standard guard:
 *   `extractItem(parsed)` → null OR item.type ∉ itemTypes ⇒ render nothing;
 *   otherwise mount the inner renderer with the unwrapped item.
 *
 * Each call also registers the inner renderer into `CODEX_RENDERERS` keyed
 * by every type in `spec.itemTypes`.
 */
export function defineCodexRenderer(spec: CodexRendererSpec): CodexMessageRenderer {
  for (const t of spec.itemTypes)
    CODEX_RENDERERS.set(t, spec.render)
  const Inner = spec.render
  const itemTypes = spec.itemTypes
  const Wrapper: CodexMessageRenderer = (props): JSX.Element => {
    const item = (): Record<string, unknown> | null => {
      const u = extractItem(props.parsed)
      if (!u || !itemTypes.includes(u.type as CodexItemType))
        return null
      return u
    }
    return (
      <Show when={item()}>
        {unwrapped => <Inner item={unwrapped()} role={props.role} context={props.context} />}
      </Show>
    )
  }
  return Wrapper
}
