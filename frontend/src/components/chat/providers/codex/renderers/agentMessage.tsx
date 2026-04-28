import { Show } from 'solid-js'
import { CODEX_ITEM } from '~/types/toolMessages'
import { MarkdownText } from '../../../messageRenderers'
import { defineCodexRenderer } from '../defineRenderer'

/** Renders Codex agentMessage items as markdown. */
export const CodexAgentMessageRenderer = defineCodexRenderer({
  itemTypes: [CODEX_ITEM.AGENT_MESSAGE],
  render: (props) => {
    const text = (): string => (props.item.text as string) || ''
    return (
      <Show when={text()}>
        <MarkdownText text={text()} />
      </Show>
    )
  },
})
