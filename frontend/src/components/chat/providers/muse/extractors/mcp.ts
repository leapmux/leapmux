import type { ToolCallSpec } from '../../../model/toolCall'
import type { MuseItemLifecycle } from './toolCommon'
import { mcpToolCallRequest } from '../../../model/mcpToolCall'
import { failedResult, unparsedResult } from '../../../model/toolCall'

interface MuseMcpFacts extends MuseItemLifecycle {
  server: string
  tool: string
  args: Record<string, unknown>
  status: string
  output: string
}

/** Keep the native preview as text. Its JSON syntax does not prove structured Model Context Protocol content. */
export function museMcpSpec(input: MuseMcpFacts): ToolCallSpec<'mcp'> {
  const failed = input.facts.providerOutcome === 'failed' || input.facts.providerOutcome === 'declined'
  const unknown = input.nativeFinal && input.facts.providerOutcome === null
  return {
    ...mcpToolCallRequest(input.server, input.tool, input.args),
    ...(unknown
      ? {
          metadata: [
            { label: 'Native status', value: input.status || 'Unspecified' },
            ...(input.output ? [{ label: 'Native output', value: input.output }] : []),
          ],
        }
      : {}),
    ...(input.facts.resultFrameLanded ? { result: failed ? failedResult(input.output) : unparsedResult(input.output) } : {}),
  }
}
