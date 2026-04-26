import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Brain from 'lucide-solid/icons/brain'
import { ThinkingBubble } from '../../../messageRenderers'
import { extractItem } from '../renderHelpers'

/** Renders Codex reasoning items with expandable content. */
export function codexReasoningRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'reasoning')
    return null

  const summary = (item.summary as string[]) || []
  const content = (item.content as string[]) || []
  const liveStream = () => context?.commandStream?.() ?? []
  const liveSummary = () => {
    const parts: string[] = []
    let current = ''
    for (const segment of liveStream()) {
      if (segment.kind === 'reasoning_summary_break') {
        if (current) {
          parts.push(current)
          current = ''
        }
        continue
      }
      if (segment.kind === 'reasoning_summary')
        current += segment.text
    }
    if (current)
      parts.push(current)
    return parts
  }
  const liveContent = () => liveStream()
    .filter(segment => segment.kind === 'reasoning_content')
    .map(segment => segment.text)
  const text = () => {
    const streamedSummary = liveSummary()
    if (streamedSummary.length > 0)
      return streamedSummary.join('\n\n')
    const streamedContent = liveContent()
    if (streamedContent.length > 0)
      return streamedContent.join('\n')
    return summary.join('\n') || content.join('\n') || ''
  }
  if (!text())
    return null

  return <ThinkingBubble text={text()} icon={Brain} label="Thinking" stateKey="codex-reasoning" context={context} />
}
