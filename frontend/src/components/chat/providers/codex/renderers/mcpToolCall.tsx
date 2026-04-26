import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Wrench from 'lucide-solid/icons/wrench'
import { useSharedExpandedState } from '../../../messageRenderers'
import { McpToolCallBody, mcpToolCallDisplayName } from '../../../results/mcpToolCall'
import { ToolUseLayout } from '../../../toolRenderers'
import { codexMcpFromItem } from '../extractors/mcp'
import { extractItem } from '../renderHelpers'
import { codexStatusTitle } from './statusTitle'

/**
 * Renders Codex `mcpToolCall` and `dynamicToolCall` items via the shared
 * `McpToolCallBody`. Status from the wire format selects the header label;
 * the body component handles args + content blocks + error rendering.
 */
export function codexMcpToolCallRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  const source = codexMcpFromItem(item)
  if (!source)
    return null

  const isTerminal = source.status === 'completed' || source.status === 'failed'
  const titleEl = codexStatusTitle(mcpToolCallDisplayName(source), source.status === 'inProgress' ? '' : source.status)
  const [expanded, setExpanded] = useSharedExpandedState(() => context, 'codex-mcp-tool-call', () => isTerminal)

  return (
    <ToolUseLayout
      icon={Wrench}
      toolName="MCP Tool Call"
      title={titleEl}
      context={context}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
      alwaysVisible={isTerminal}
    >
      <McpToolCallBody source={source} context={context} />
    </ToolUseLayout>
  )
}
