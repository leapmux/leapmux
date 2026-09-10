import Brain from 'lucide-solid/icons/brain'
import { createMemo, For, Show } from 'solid-js'
import { stringArray } from '~/lib/jsonPick'
import { CODEX_ITEM } from '~/types/toolMessages'
import { MarkdownText, ThinkingBubble } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { defineCodexRenderer } from '../defineRenderer'

/** Renders Codex reasoning items with expandable content. */
export const CodexReasoningRenderer = defineCodexRenderer({
  itemTypes: [CODEX_ITEM.REASONING],
  render: (props) => {
    const summary = createMemo(() => stringArray(props.item.summary).filter(item => item.trim().length > 0))
    const content = createMemo(() => stringArray(props.item.content).join('\n'))
    const hasContent = createMemo(() => summary().length > 0 || content().length > 0)

    const renderBody = () => {
      const summaryItems = summary()
      if (summaryItems.length === 0)
        return <MarkdownText text={content()} context={props.context} />

      return (
        <ul>
          <For each={summaryItems}>
            {item => (
              <li>
                <MarkdownText text={item} context={props.context} />
              </li>
            )}
          </For>
        </ul>
      )
    }

    return (
      <Show when={hasContent()}>
        <ThinkingBubble renderBody={renderBody} icon={Brain} label="Thinking" stateKey={props.context?.expandUiKey ?? MESSAGE_UI_KEY.CODEX_REASONING} context={props.context} />
      </Show>
    )
  },
})
