import Brain from 'lucide-solid/icons/brain'
import { createMemo, Show } from 'solid-js'
import { CODEX_ITEM } from '~/types/toolMessages'
import { ThinkingBubble } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { defineCodexRenderer } from '../defineRenderer'

/** Renders Codex reasoning items with expandable content. */
export const CodexReasoningRenderer = defineCodexRenderer({
  itemTypes: [CODEX_ITEM.REASONING],
  render: (props) => {
    const summary = (): string[] => (props.item.summary as string[]) || []
    const content = (): string[] => (props.item.content as string[]) || []
    const liveStream = createMemo(() => props.context?.commandStream?.() ?? [])
    const liveSummary = createMemo(() => {
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
    })
    const liveContent = createMemo(() => liveStream()
      .filter(segment => segment.kind === 'reasoning_content')
      .map(segment => segment.text))
    const text = createMemo(() => {
      const streamedSummary = liveSummary()
      if (streamedSummary.length > 0)
        return streamedSummary.join('\n\n')
      const streamedContent = liveContent()
      if (streamedContent.length > 0)
        return streamedContent.join('\n')
      return summary().join('\n') || content().join('\n') || ''
    })

    return (
      <Show when={text()}>
        <ThinkingBubble text={text()} icon={Brain} label="Thinking" stateKey={MESSAGE_UI_KEY.CODEX_REASONING} context={props.context} />
      </Show>
    )
  },
})
