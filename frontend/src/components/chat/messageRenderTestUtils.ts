import type { JSXElement } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { render } from '@solidjs/testing-library'
import { renderNotificationThread } from './notificationRenderers'
import { renderResultDivider } from './resultDividerRenderers'

// Shared render-and-probe helpers for the two shared message render paths:
// `renderNotificationThread(messages, provider?)` (standalone or consolidated
// notification) and `renderResultDivider(parsed, provider)` (turn-end divider).
// Centralizing the render + trim + icon/danger-color probe here keeps that shape
// in one place instead of re-deriving `container.textContent` / `style.color` in
// each test file.

/** Render a JSX element and return its trimmed text content ('' when null). */
export function elementText(el: JSXElement | null): string {
  if (el === null)
    return ''
  return render(() => el).container.textContent?.trim() ?? ''
}

/** Render a notification list (optionally with a provider) to trimmed text. */
export function renderThreadText(messages: unknown[], provider?: AgentProvider): string {
  return elementText(renderNotificationThread(messages, provider))
}

/** True when the rendered notification list carries the compaction divider icon. */
export function renderThreadHasIcon(messages: unknown[], provider?: AgentProvider): boolean {
  const el = renderNotificationThread(messages, provider)
  if (el === null)
    return false
  return render(() => el).container.querySelector('svg') !== null
}

/**
 * Render a result divider for `parsed` under `provider` and return its trimmed
 * text plus whether it is danger-styled. Centralizes the divider DOM probe (text
 * + the `var(--danger)` color check) so every provider's divider test asserts the
 * same way instead of re-deriving `querySelector('div').style.color`. `isError`
 * is false when the divider hook returns null (nothing rendered).
 */
export function renderDivider(parsed: unknown, provider: AgentProvider): { text: string, isError: boolean } {
  const el = renderResultDivider(parsed, provider)
  if (el === null)
    return { text: '', isError: false }
  const { container } = render(() => el)
  return {
    text: container.textContent?.trim() ?? '',
    isError: container.querySelector('div')?.style.color === 'var(--danger)',
  }
}
