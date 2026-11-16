import type { JSX } from 'solid-js'
import type { RequestSideCall } from './renderer'
import Brain from 'lucide-solid/icons/brain'
import { proseRenderer, typedRequestLine } from './proseResult'
import { previewWithoutResult } from './renderer'

export const thinkRenderer = proseRenderer<'think'>({
  icon: Brain,
  label: 'Think',
  title(call) {
    return call.title ?? 'Think'
  },
  // The thought's first line, while the call runs. The result row then draws the
  // answer, or the line again when the call ended with none.
  pending: thinkPreview,
  hasPending: call => firstLine(call.request.text) !== undefined,
  withoutTypedResult: previewWithoutResult(thinkPreview),
})

function thinkPreview(call: RequestSideCall<'think'>): JSX.Element | null {
  return typedRequestLine([
    firstLine(call.request.text),
  ])
}

function firstLine(text: string): string | undefined {
  const line = text.split('\n', 1)[0]?.trim()
  return line || undefined
}
