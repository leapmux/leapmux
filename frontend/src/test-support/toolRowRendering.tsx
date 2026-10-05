import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { render } from '@solidjs/testing-library'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { providerRow } from '~/test-support/toolCallFixture'

/**
 * The frames of one finished call, and the supplement that the Worker stored beside
 * its opening frame.
 */
export interface ToolRowFrames {
  /** The frame that opened the call. */
  opening: Record<string, unknown>
  /** The frame that ended the call. */
  ending: Record<string, unknown>
  /** The supplement of the stored request row: the late input fields and the identity of the opening frame. */
  requestSupplement?: Record<string, unknown>
  /** The `span_type` that the Worker stores on both rows. */
  spanType: string
}

/**
 * Render the two rows of ONE finished call APART, each as the message store resolves it.
 *
 * A paired result row draws no header (`ToolMessageLayout`), so only the request row
 * can state the file or the command of the call. A container that holds both rows
 * cannot tell which row drew a word. The e2e specs read one row by its
 * `data-tool-row-role`, so a test of such a spec needs the rows apart.
 *
 * The supplement reaches the request row as the Worker stores it. It reaches the result
 * row through the request side of the span. The caller registers the plugin of the
 * provider.
 */
export function renderToolRows(provider: AgentProvider, frames: ToolRowFrames): { request: HTMLElement, result: HTMLElement } {
  const stored = (frame: Record<string, unknown>, supplementalContent?: Record<string, unknown>): ParsedMessageContent => ({ rawText: '', topLevel: frame, parentObject: frame, wrapper: null, supplementalContent })
  const requestRow = providerRow(provider, frames.opening, { role: 'request', spanType: frames.spanType, supplementalContent: frames.requestSupplement, result: stored(frames.ending) })
  const resultRow = providerRow(provider, frames.ending, { role: 'result', spanType: frames.spanType, request: stored(frames.opening, frames.requestSupplement) })
  if (requestRow?.kind !== 'tool' || resultRow?.kind !== 'tool')
    throw new Error('A finished call is a tool row on each side.')
  return {
    request: render(() => <ToolMessage row={requestRow} />).container,
    result: render(() => <ToolMessage row={resultRow} />).container,
  }
}
