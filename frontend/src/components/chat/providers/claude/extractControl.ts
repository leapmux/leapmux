import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { isObject, pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { CLAUDE_TOOL_NAMES } from './toolNames'

/**
 * The tool permissions a plan approval asks for, beside the plan itself.
 *
 * Claude states them as `allowedPrompts: [{tool, prompt}]`. A pair with either half
 * blank states no permission a reader could weigh, so it is dropped rather than
 * drawn as an empty row.
 */
function claudePlanPermissions(payload: Record<string, unknown>) {
  const input = getToolInput(payload)
  if (!Array.isArray(input.allowedPrompts))
    return []
  return input.allowedPrompts.flatMap((value) => {
    if (!isObject(value))
      return []
    const tool = pickString(value, 'tool')
    const prompt = pickString(value, 'prompt')
    return tool.trim() && prompt.trim() ? [{ tool, prompt }] : []
  })
}

/**
 * `Provider.extractControl` for Claude Code.
 *
 * Claude multiplexes every prompt over one request shape and states which one arrived
 * by its TOOL NAME, so the tool name is what this switches on -- the same
 * discriminator its control component used.
 */
export function claudeExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  // A question is NOT stated here. `askUserQuestion.isRequest` is the one recognizer,
  // and `controlSurface` asks it first -- so a `question` returned here only ever
  // reached a line that threw it away.
  const toolName = getToolName(payload)
  if (toolName === CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE)
    return { kind: 'plan', permissions: claudePlanPermissions(payload) }
  const toolInput = getToolInput(payload)
  // A shell call states its command, which the banner draws as code above the
  // arguments. One shared body spelled `'Bash'` inline for every provider that
  // reached it; the tool table owns the name, and PowerShell is the same call on
  // Windows.
  const command = toolName === CLAUDE_TOOL_NAMES.BASH || toolName === CLAUDE_TOOL_NAMES.POWERSHELL
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
