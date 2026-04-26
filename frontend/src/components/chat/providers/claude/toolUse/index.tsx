import type { JSX } from 'solid-js'
import type { MessageCategory } from '../../../messageClassification'
import type { RenderContext } from '../../../messageRenderers'
import type { BashInput } from '~/types/toolMessages'
import { isObject } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { formatToolInput } from '../../../rendererUtils'
import { renderAskUserQuestion } from './askUserQuestion'
import { renderExitPlanMode } from './exitPlanMode'
import { ToolUseMessage } from './genericToolUse'
import { toolIconFor } from './icons'
import { deriveToolSummary } from './summary'
import { renderClaudeToolTitle } from './title'
import { renderTodoWrite } from './todoWrite'

/** Render a Claude tool_use category. */
export function renderClaudeToolUse(
  category: Extract<MessageCategory, { kind: 'tool_use' }>,
  _parsed: unknown,
  context?: RenderContext,
): JSX.Element | null {
  const toolName = category.toolName
  const toolUse = category.toolUse

  // Special tool_use renderers
  if (toolName === CLAUDE_TOOL.TODO_WRITE)
    return renderTodoWrite(toolUse, context)
  if (toolName === CLAUDE_TOOL.ASK_USER_QUESTION)
    return renderAskUserQuestion(toolUse, context)
  if (toolName === CLAUDE_TOOL.EXIT_PLAN_MODE)
    return renderExitPlanMode(toolUse, context)

  // Generic tool_use rendering
  const input = isObject(toolUse.input) ? toolUse.input as Record<string, unknown> : {}
  const title = renderClaudeToolTitle(toolName, input, context)
  const summary = deriveToolSummary(toolName, input, context)
  const fallbackDisplay = title ? null : formatToolInput(toolUse.input)

  // Edit/Write tool_use messages render only the header — the diff lives on the
  // result message (see renderClaudeToolResult), which falls back to the
  // tool_use input when its own payload carries no diff.

  // Bash: pass full command for multi-line expand.
  const fullCommand = toolName === CLAUDE_TOOL.BASH ? (input as BashInput).command : undefined

  return (
    <ToolUseMessage
      toolName={toolName}
      icon={toolIconFor(toolName)}
      title={title}
      summary={summary}
      fullCommand={fullCommand}
      fallbackDisplay={fallbackDisplay}
      context={context}
    />
  )
}
