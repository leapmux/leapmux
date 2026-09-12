import type { McpToolCallSource } from '../../../results/mcpToolCall'
import type { ZCodeRow } from './toolCommon'
import { prettifyArgsJson, prettifyStructuredJson } from '~/lib/jsonFormat'
import { pickObject, pickString } from '~/lib/jsonPick'
import { formatTaskStatus } from '../../../rendererUtils'
import { ZCODE_DISPLAY } from '../protocol'
import { zcodeExtractTool, zcodeToolInput } from './toolCommon'
import { zcodeDisplayImages, zcodeMcpContent } from './toolContent'

export type ZCodeResultDisplay
  = | { kind: 'mcp', source: McpToolCallSource, truncated: boolean }
    | { kind: 'status', title: string, output: string, command?: string, status: 'success' | 'failed' | 'waiting' | 'stopped', truncated: boolean }
    | { kind: 'images', output: string, truncated: boolean }

/** Adapt ZCode display hints to the shared tool components. */
export function zcodeResultDisplay(row: ZCodeRow): ZCodeResultDisplay | null {
  const update = zcodeExtractTool(row.parsed)
  const display = update?.result?.display
  if (!display || !update)
    return null
  const content = update.result?.content ?? ''
  const truncated = display.truncated === true || update.result?.truncated === true
  switch (display.kind) {
    case ZCODE_DISPLAY.NodeImages:
      return { kind: 'images', output: content, truncated }
    case ZCODE_DISPLAY.McpTool:
    case ZCODE_DISPLAY.ComputerUse: {
      const computer = display.kind === ZCODE_DISPLAY.ComputerUse
      const args = zcodeToolInput(row)
      const text = pickString(display, 'text') || content
      const error = pickString(display, 'errorCode') || pickString(pickObject(display, 'unavailable'), 'code')
      const suggestion = pickString(display, 'suggestedAction')
      const failed = update.isError || display.status === 'failed' || !!error
      const targetApp = pickString(pickObject(display, 'targetApp'), 'displayName')
      const tool = pickString(display, 'toolName') || row.toolName
      return {
        kind: 'mcp',
        truncated,
        source: {
          server: computer ? 'Computer use' : pickString(display, 'serverName'),
          tool: targetApp ? `${tool} (${targetApp})` : tool,
          argsJson: prettifyArgsJson(pickString(display, 'input') || args),
          content: zcodeMcpContent(row) ?? [
            ...(text ? [{ type: 'text' as const, text }] : []),
            ...zcodeDisplayImages(display).map(source => ({ type: 'image' as const, source })),
          ],
          structuredJson: prettifyStructuredJson(display.structuredContent),
          error: [error, suggestion].filter(Boolean).join('\n') || undefined,
          status: failed ? 'failed' : 'completed',
          durationMs: update.durationMs ?? undefined,
        },
      }
    }
    case ZCODE_DISPLAY.TaskOutput: {
      const retrieval = pickString(display, 'retrievalStatus')
      const taskStatus = pickString(display, 'taskStatus')
      return {
        kind: 'status',
        title: formatTaskStatus(taskStatus || undefined) || (retrieval === 'timeout' ? 'Timed out' : retrieval === 'not_ready' ? 'Not ready' : 'Task output'),
        output: pickString(display, 'output') || content,
        status: taskStatus === 'failed' || update.isError ? 'failed' : taskStatus === 'completed' ? 'success' : 'waiting',
        truncated,
      }
    }
    case ZCODE_DISPLAY.TaskStop:
      return {
        kind: 'status',
        title: `Stopped task ${pickString(display, 'taskId')}`.trim(),
        output: pickString(display, 'message') || content,
        command: pickString(display, 'command', undefined),
        status: update.isError ? 'failed' : 'stopped',
        truncated,
      }
    case ZCODE_DISPLAY.LocalAgentMessage:
    case ZCODE_DISPLAY.RespondToCoordinator: {
      const failed = update.isError || display.status === 'failed'
      const error = pickString(display, 'error')
      const message = pickString(display, 'message') || content
      return {
        kind: 'status',
        title: failed ? 'Failed' : 'Message sent',
        output: [error, message].filter(Boolean).join('\n'),
        status: failed ? 'failed' : 'success',
        truncated,
      }
    }
    default:
      return null
  }
}
