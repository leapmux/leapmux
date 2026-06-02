import type { JSXElement } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { render } from '@solidjs/testing-library'
import { renderNotificationThread } from './notificationRenderers'

// Shared render helpers for notification tests. The notification render path is
// `renderNotificationThread(messages, provider?)` for both a standalone
// notification (a one-element list) and a consolidated thread, so every
// notification test -- provider-neutral or per-provider -- renders the same way.
// Centralizing the render + trim + icon-probe here keeps that shape in one place
// instead of re-deriving it in each test file.

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
