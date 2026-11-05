import type { McpRequest, McpResult } from '../../../model/tools/mcp'
import type { AntigravityStep } from './toolCommon'
import { pickObject, pickString } from '~/lib/jsonPick'
import { antigravityMcpImages } from './read'

export function antigravityMcpRequest(step: AntigravityStep): McpRequest {
  const mcp = pickObject(step.raw, 'mcpTool')
  return {
    server: pickString(mcp, 'serverName'),
    tool: pickString(pickObject(mcp, 'toolCall'), 'name') || pickString(step.toolCall, 'name'),
    args: step.args,
  }
}

export function antigravityMcpResult(step: AntigravityStep): McpResult {
  return {
    content: [
      {
        type: 'text',
        text: pickString(pickObject(step.raw, 'mcpTool'), 'resultString'),
      },
      ...antigravityMcpImages(step).map(source => ({ type: 'image' as const, source })),
    ],
  }
}
