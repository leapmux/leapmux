import { createMemo, Show } from 'solid-js'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { messageCompletionFromProto } from '../../../assembledMessage'
import { McpToolMessage } from '../../../results/McpToolMessage'
import { defineCodexRenderer } from '../defineRenderer'
import { codexMcpFromItem } from '../extractors/mcp'
import { extractItem } from '../renderHelpers'

/**
 * Renders Codex `mcpToolCall` and `dynamicToolCall` items via the shared
 * `McpToolCallBody`. Status from the wire format selects the header label;
 * the body component handles args + content blocks + error rendering.
 */
export const CodexMcpToolCallRenderer = defineCodexRenderer({
  itemTypes: [CODEX_ITEM.MCP_TOOL_CALL, CODEX_ITEM.DYNAMIC_TOOL_CALL],
  render: (props) => {
    const source = createMemo(() => codexMcpFromItem(props.item))
    const hasRequest = () => {
      const request = extractItem(props.context?.sources?.request()?.parentObject)
      return !!props.item.id && request?.id === props.item.id && request.type === props.item.type && request.status === CODEX_STATUS.IN_PROGRESS
    }

    return (
      <Show when={source()}>
        {s => (
          <McpToolMessage
            source={s()}
            role={s().status === CODEX_STATUS.IN_PROGRESS && !messageCompletionFromProto(props.context?.sources?.current()?.completion) ? 'request' : 'result'}
            hasRequest={hasRequest()}
            context={props.context}
          />
        )}
      </Show>
    )
  },
})
