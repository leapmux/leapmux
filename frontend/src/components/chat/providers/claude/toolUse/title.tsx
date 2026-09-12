import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { BashInput, EditInput, GlobInput, GrepInput, ReadInput, RemoteTriggerInput, SendMessageInput, TaskStopInput, ToolSearchInput, WebFetchInput, WebSearchInput, WriteInput } from '~/types/toolMessages'
import { pickString } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { joinMetaParts } from '../../../rendererUtils'
import { mcpToolCallDisplayName } from '../../../results/mcpToolCall'
import { toolInputCode, toolInputText } from '../../../toolStyles.css'
import {
  renderAgentTitle,
  renderBashTitle,
  renderEditTitle,
  renderGlobTitle,
  renderMcpTitle,
  renderQueryTitle,
  renderReadTitle,
  renderSearchTitle,
  renderUrlTitle,
  renderWriteTitle,
  toolInputHint,
} from '../../../toolTitleRenderers'
import { parseClaudeMcpToolName } from '../extractors/mcp'
import { renderSendMessageTitle } from './sendMessage'

export function renderClaudeToolTitle(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const cwd = context?.workingDir
  const homeDir = context?.homeDir

  switch (toolName) {
    // Shared tool title primitives
    case CLAUDE_TOOL.BASH: return renderBashTitle((input as BashInput).description, (input as BashInput).command)
    case CLAUDE_TOOL.READ: return renderReadTitle((input as ReadInput).file_path, (input as ReadInput).offset, (input as ReadInput).limit, cwd, homeDir)
    case CLAUDE_TOOL.WRITE: return renderWriteTitle((input as WriteInput).file_path, (input as WriteInput).content, cwd, homeDir)
    case CLAUDE_TOOL.EDIT: return renderEditTitle((input as EditInput).file_path, (input as EditInput).old_string, (input as EditInput).new_string, (input as EditInput).replace_all, cwd, homeDir)
    // Title shows just the pattern; the path is rendered as the summary
    // (second line) by deriveToolSummary so we don't duplicate it.
    case CLAUDE_TOOL.GREP: return renderSearchTitle((input as GrepInput).pattern, undefined, cwd, homeDir)
    case CLAUDE_TOOL.GLOB: return renderGlobTitle((input as GlobInput).pattern, (input as GlobInput).path, cwd, homeDir)
    case CLAUDE_TOOL.WEB_FETCH: return renderUrlTitle((input as WebFetchInput).url)
    case CLAUDE_TOOL.WEB_SEARCH: return renderQueryTitle((input as WebSearchInput).query)
    case CLAUDE_TOOL.AGENT:
    case CLAUDE_TOOL.TASK: return renderAgentTitle(String(input.description || toolName), input.subagent_type ? String(input.subagent_type) : undefined)

    // Claude-only tool titles
    case CLAUDE_TOOL.SEND_MESSAGE:
      return renderSendMessageTitle(input as SendMessageInput, context)
    case CLAUDE_TOOL.LIST_AGENTS: {
      // Both filters are optional and usually absent, so the bare label is the
      // normal case rather than a fallback. pickString rather than a cast: the
      // model writes this input, and a non-string that survives a bare cast is
      // still truthy, so it template-interpolates into `channel: [object
      // Object]` where an absent field correctly yields the plain label.
      const channel = pickString(input, 'channel').trim()
      const q = pickString(input, 'q').trim()
      const filters = joinMetaParts([
        channel && `channel: ${channel}`,
        q && `matching: ${q}`,
      ])
      return <span class={toolInputText}>{filters ? `List agents (${filters})` : 'List agents'}</span>
    }
    case CLAUDE_TOOL.TASK_OUTPUT: {
      const { task_id, block, timeout } = input as { task_id?: string, block?: boolean, timeout?: number }
      const inner = joinMetaParts([
        task_id && `task ID: ${task_id}`,
        typeof timeout === 'number' && `timeout: ${timeout >= 1000 ? `${timeout / 1000}s` : `${timeout}ms`}`,
        block !== undefined && `block: ${block}`,
      ])
      const meta = inner ? ` (${inner})` : ''
      return <span class={toolInputText}>{`Waiting for output${meta}`}</span>
    }
    case CLAUDE_TOOL.TOOL_SEARCH: {
      const { query } = input as ToolSearchInput
      return query
        ? <span class={toolInputCode}>{`"${query}"`}</span>
        : null
    }
    case CLAUDE_TOOL.TASK_STOP: {
      const { task_id: taskId } = input as TaskStopInput
      return taskId
        ? <span class={toolInputText}>{`Stop task ${taskId}`}</span>
        : <span class={toolInputText}>Stop task</span>
    }
    case CLAUDE_TOOL.ENTER_PLAN_MODE:
      return <span class={toolInputText}>Entering Plan Mode</span>
    case CLAUDE_TOOL.SKILL: {
      const skillName = String(input.skill || '')
      return <span class={toolInputText}>{`Skill: /${skillName}`}</span>
    }
    case CLAUDE_TOOL.REMOTE_TRIGGER: {
      const { action, trigger_id: triggerId, body } = input as RemoteTriggerInput
      const name = typeof body?.name === 'string' ? body.name : ''
      const label = (() => {
        switch (action) {
          case 'list': return 'List triggers'
          case 'get': return triggerId ? `Get trigger ${triggerId}` : 'Get trigger'
          case 'create': return name ? `Create trigger: ${name}` : 'Create trigger'
          case 'update': {
            const head = triggerId ? `Update trigger ${triggerId}` : 'Update trigger'
            return name ? `${head}: ${name}` : head
          }
          case 'run': return triggerId ? `Run trigger ${triggerId}` : 'Run trigger'
          default: return 'RemoteTrigger'
        }
      })()
      return <span class={toolInputText}>{label}</span>
    }
    default: {
      const hint = toolInputHint(input)
      const mcpInfo = parseClaudeMcpToolName(toolName)
      if (mcpInfo) {
        const displayName = mcpToolCallDisplayName({ server: mcpInfo.serverName, tool: mcpInfo.toolName })
        return renderMcpTitle(displayName, input)
      }
      // Unknown non-MCP tool — show tool name with hint if available.
      return hint
        ? <span class={toolInputText}>{`${toolName}: ${hint}`}</span>
        : <span class={toolInputText}>{toolName}</span>
    }
  }
}
