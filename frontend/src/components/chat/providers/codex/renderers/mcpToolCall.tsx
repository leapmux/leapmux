import Wrench from 'lucide-solid/icons/wrench'
import { createMemo, Show } from 'solid-js'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { McpToolCallBody, mcpToolCallDisplayName } from '../../../results/mcpToolCall'
import { ToolUseLayout } from '../../../toolRenderers'
import { defineCodexRenderer } from '../defineRenderer'
import { codexMcpFromItem } from '../extractors/mcp'
import { isCodexTerminalStatus } from '../status'
import { codexStatusTitle } from './statusTitle'

/**
 * Renders Codex `mcpToolCall` and `dynamicToolCall` items via the shared
 * `McpToolCallBody`. Status from the wire format selects the header label;
 * the body component handles args + content blocks + error rendering.
 */
export const CodexMcpToolCallRenderer = defineCodexRenderer({
  itemTypes: [CODEX_ITEM.MCP_TOOL_CALL, CODEX_ITEM.DYNAMIC_TOOL_CALL],
  render: (props) => {
    const source = createMemo(() => codexMcpFromItem(props.item))
    const isTerminal = (): boolean => isCodexTerminalStatus(source()?.status)
    const titleEl = (): ReturnType<typeof codexStatusTitle> | null => {
      const s = source()
      return s ? codexStatusTitle(mcpToolCallDisplayName(s), s.status === CODEX_STATUS.IN_PROGRESS ? '' : s.status) : null
    }
    const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.CODEX_MCP_TOOL_CALL, isTerminal)

    return (
      <Show when={source()}>
        {s => (
          <ToolUseLayout
            icon={Wrench}
            toolName="MCP Tool Call"
            title={titleEl()}
            context={props.context}
            expanded={expanded()}
            onToggleExpand={() => setExpanded(v => !v)}
            alwaysVisible={isTerminal()}
          >
            <McpToolCallBody source={s()} context={props.context} />
          </ToolUseLayout>
        )}
      </Show>
    )
  },
})
