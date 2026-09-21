import type { JSXElement } from 'solid-js'
import type { ResolvedMessageContent } from '~/components/chat/rowExtractionTypes'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { render } from '@solidjs/testing-library'
import { flattenNotificationEntries, notificationEntriesFor } from '~/components/chat/notificationEntries'
import { renderNotificationBlocks } from '~/components/chat/notificationRenderers'
import { resolveMessageForRendering } from '~/components/chat/providers/registry'
import { ResultDivider } from '~/components/chat/resultDividerRenderers'
import { extractChatRow, extractedRow } from '~/components/chat/rowExtraction'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'

// Shared render-and-probe helpers for the two shared message render paths: the
// notification thread (standalone or consolidated) and the turn-end divider. Each
// one reads its row through the SAME `extractChatRow` the app uses, and draws it
// with the same renderer, so a test can never assert against a path production does
// not take.
// Centralizing the render + trim + icon/danger-color probe here keeps that shape
// in one place instead of re-deriving `container.textContent` / `style.color` in
// each test file.

/** The parse a category-driven extraction does not read. Both helpers state their row's category. */
function parsedOf(parsed: unknown, provider?: AgentProvider): ResolvedMessageContent {
  const parentObject = isObject(parsed) ? parsed : undefined
  return resolveMessageForRendering({ wrapper: null, topLevel: parentObject ?? null, parentObject, rawText: '', supplementalContent: undefined, messageMetadata: undefined }, provider ?? AgentProvider.CLAUDE_CODE)
}

/**
 * The element one notification row draws, through the two calls `rowRenderers` makes.
 *
 * `extractChatRow` reads each message of the thread into the row's entries, and
 * `renderNotificationBlocks` draws the blocks they flatten to. A helper that composed
 * the entries itself would be a second source of truth for the one thing these tests
 * are about. Null when the thread states nothing, exactly as the transcript row is.
 */
export function renderThreadElement(messages: unknown[], provider?: AgentProvider): JSXElement | null {
  const agentProvider = provider ?? AgentProvider.CLAUDE_CODE
  const entries = messages.flatMap(message => isObject(message) ? notificationEntriesFor(message, agentProvider) : [])
  const row = extractedRow(extractChatRow(provider, parsedOf(undefined, provider), { kind: 'notification', entries }, {}))
  return row?.kind === 'notification' ? renderNotificationBlocks(flattenNotificationEntries(row.thread.entries)) : null
}

/** Render a JSX element and return its trimmed text content ('' when null). */
export function elementText(el: JSXElement | null): string {
  if (el === null)
    return ''
  return render(() => el).container.textContent?.trim() ?? ''
}

/** Render a notification list (optionally with a provider) to trimmed text. */
export function renderThreadText(messages: unknown[], provider?: AgentProvider): string {
  return elementText(renderThreadElement(messages, provider))
}

/**
 * The markup of the first glyph the rendered notification list draws, or null
 * when it draws none.
 *
 * The MARKUP rather than a name, because a Lucide icon arrives as a component
 * and leaves no identifier in the DOM. Two outcomes that must look different
 * compare unequal here, which is the assertion a per-outcome glyph map needs;
 * an exact path string would pin the icon set's own drawing and break on a
 * Lucide upgrade that changes nothing about this code.
 */
export function renderThreadGlyph(messages: unknown[], provider?: AgentProvider): string | null {
  const el = renderThreadElement(messages, provider)
  if (el === null)
    return null
  return render(() => el).container.querySelector('svg')?.innerHTML ?? null
}

/** True when the rendered notification list carries the compaction divider icon. */
export function renderThreadHasIcon(messages: unknown[], provider?: AgentProvider): boolean {
  return renderThreadGlyph(messages, provider) !== null
}

/**
 * Render a result divider for `parsed` under `provider` and return its trimmed
 * text plus whether it is danger-styled. Centralizes the divider DOM probe (text
 * + the `var(--danger)` color check) so every provider's divider test asserts the
 * same way instead of re-deriving `querySelector('div').style.color`. `isError`
 * is false when the divider hook returns null (nothing rendered).
 */
export function renderDivider(parsed: unknown, provider: AgentProvider, completion?: MessageCompletion): { text: string, isError: boolean } {
  const row = extractedRow(extractChatRow(provider, parsedOf(parsed, provider), { kind: 'result_divider' }, { ...(completion !== undefined ? { completion } : {}) }))
  if (row?.kind !== 'divider')
    return { text: '', isError: false }
  const { container } = render(() => <ResultDivider model={row.divider} />)
  return {
    text: container.textContent?.trim() ?? '',
    isError: container.querySelector('div')?.style.color === 'var(--danger)',
  }
}
