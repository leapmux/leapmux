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
    const text = createMemo(() => summary().join('\n') || content().join('\n') || '')

    return (
      <Show when={text()}>
        <ThinkingBubble text={text()} icon={Brain} label="Thinking" stateKey={props.context?.expandUiKey ?? MESSAGE_UI_KEY.CODEX_REASONING} context={props.context} />
      </Show>
    )
  },
})
