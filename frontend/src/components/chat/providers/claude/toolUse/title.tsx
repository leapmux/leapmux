import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { BashInput, EditInput, GlobInput, GrepInput, ReadInput, TaskStopInput, ToolSearchInput, WebFetchInput, WebSearchInput, WriteInput } from '~/types/toolMessages'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { toolInputCode, toolInputText } from '../../../toolStyles.css'
import {
  renderAgentTitle,
  renderBashTitle,
  renderEditTitle,
  renderGlobTitle,
  renderQueryTitle,
  renderReadTitle,
  renderSearchTitle,
  renderUrlTitle,
  renderWriteTitle,
} from '../../../toolTitleRenderers'
import { formatClaudeMcpDisplayName, parseClaudeMcpToolName } from '../extractors/mcp'

/** Prefer common parameter names for the hint, then fall back to first short string. */
const HINT_KEYS = ['query', 'input', 'prompt', 'text', 'command', 'description', 'url']

function extractInputHint(input: Record<string, unknown>): string {
  for (const key of HINT_KEYS) {
    const val = input[key]
    if (typeof val === 'string' && val.length > 0 && val.length <= 120)
      return val.length > 80 ? `${val.slice(0, 80)}…` : val
  }
  for (const val of Object.values(input)) {
    if (typeof val === 'string' && val.length > 0 && val.length <= 120)
      return val.length > 80 ? `${val.slice(0, 80)}…` : val
  }
  return ''
}

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
    case CLAUDE_TOOL.TASK_OUTPUT: {
      const { task_id, block, timeout } = input as { task_id?: string, block?: boolean, timeout?: number }
      const parts: string[] = []
      if (task_id)
        parts.push(`task ID: ${task_id}`)
      if (typeof timeout === 'number')
        parts.push(`timeout: ${timeout >= 1000 ? `${timeout / 1000}s` : `${timeout}ms`}`)
      if (block !== undefined)
        parts.push(`block: ${block}`)
      const meta = parts.length > 0 ? ` (${parts.join(' · ')})` : ''
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
    default: {
      const hint = extractInputHint(input)
      const mcpInfo = parseClaudeMcpToolName(toolName)
      if (mcpInfo) {
        const displayName = formatClaudeMcpDisplayName(mcpInfo.serverName, mcpInfo.toolName)
        return (
          <>
            <span class={toolInputText}>{displayName}</span>
            {hint ? <span class={toolInputCode}>{` "${hint}"`}</span> : null}
          </>
        )
      }
      // Unknown non-MCP tool — show tool name with hint if available.
      return hint
        ? <span class={toolInputText}>{`${toolName}: ${hint}`}</span>
        : <span class={toolInputText}>{toolName}</span>
    }
  }
}
