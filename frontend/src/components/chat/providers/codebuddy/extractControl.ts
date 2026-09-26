import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'

/** The CodeBuddy tool names this extractor draws with a shell command. */
const CODEBUDDY_SHELL_TOOLS = new Set(['Bash'])

/**
 * `Provider.extractControl` for CodeBuddy Code.
 *
 * CodeBuddy's stream is Claude Code-shaped, so a can_use_tool request states a
 * tool name and its input, and a shell call states its command. The plan tools
 * map onto the plan surface; everything else is a permission the shared
 * Allow/Deny pair answers, because the worker translates that pair into
 * CodeBuddy's own `{"allowed":true}` answer.
 */
export function codebuddyExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  const toolName = getToolName(payload)
  if (toolName === 'ExitPlanMode')
    return { kind: 'plan' }
  const toolInput = getToolInput(payload)
  const command = CODEBUDDY_SHELL_TOOLS.has(toolName)
    ? pickString(toolInput, 'command', undefined)
    : undefined
  return {
    kind: 'permission',
    permission: {
      title: toolName,
      input: toolInput,
      ...(command !== undefined ? { command } : {}),
      options: [],
    },
  }
}
