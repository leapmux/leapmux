import type { ToolCallPayload } from '../../../ir/toolCall'
import type { SwitchModeRequest } from '../../../ir/tools/switchMode'
import type { ClaudeToolRow } from './toolCommon'
import { pickString } from '~/lib/jsonPick'
import { proseResult, unparsedResult } from '../../../ir/toolCall'
import { CLAUDE_TOOL_NAMES } from '../toolNames'

/**
 * The mode-switch pair: which mode the session took, and what the switch said.
 *
 * `ExitPlanMode` is the one call whose refusal is an ANSWER: the reader sent the
 * plan back with feedback, and the command line interface flags the call `is_error`
 * only because it did not proceed. The declined branch therefore words that outcome
 * itself, through `SwitchModeRequest.declinedTitle`. The shared header knows no
 * provider, so it cannot tell this tool from one whose refusal really is a failure --
 * Pi's, which answers `failed`.
 */
export function claudeSwitchModePayload(request: SwitchModeRequest, args: ClaudeToolRow, result: ClaudeToolRow | undefined): ToolCallPayload<'switch_mode'> {
  const toolName = args.toolName
  const exitPlan = toolName === CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE
  // The header word rides only when the tool states one; absent lets `request.mode`
  // word the header instead.
  const titleBefore = claudeSwitchModeTitle(toolName, false)
  const titleAfter = claudeSwitchModeTitle(toolName, true)
  if (!result)
    return { kind: 'switch_mode', request, ...(titleBefore !== undefined ? { title: titleBefore } : {}) }
  if (exitPlan && result.isError === true) {
    return {
      kind: 'switch_mode',
      request: { ...request, declinedTitle: 'Sent feedback' },
      ...(titleBefore !== undefined ? { title: titleBefore } : {}),
      statusOverride: 'declined',
      result: proseResult(result.resultContent),
    }
  }
  // The file the command line interface wrote the approved plan to. The row used to
  // state it under its header, and only the RESULT carries it. A labelled fact rather
  // than a body line, because the body holds whatever the call itself returned.
  const planFile = exitPlan ? pickString(result.toolUseResult, 'filePath') : ''
  const metadata = planFile ? [{ label: 'Plan file', value: planFile }] : undefined
  const header = {
    ...(titleAfter !== undefined ? { title: titleAfter } : {}),
    ...(metadata !== undefined ? { metadata } : {}),
  }
  if (!result.resultContent)
    return { kind: 'switch_mode', request, ...header, result: unparsedResult(result.resultContent) }
  return { kind: 'switch_mode', request, ...header, result: proseResult(result.resultContent) }
}

/**
 * The words one switch puts in its own header, or none when `mode` states it.
 *
 * `switchModeRenderer` titles the row from `request.mode` FIRST and reads this only
 * when the request states no mode, so the two tools that carry a human sentence state
 * no mode at all -- the Codex review markers omit theirs for the same reason. The
 * plan brackets are those two: `default` is the mode the session RETURNS to, which
 * says nothing about the plan that just ended.
 */
function claudeSwitchModeTitle(toolName: string, answered: boolean): string | undefined {
  switch (toolName) {
    case CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE:
      return answered ? 'Plan approved' : 'Exit plan mode'
    case CLAUDE_TOOL_NAMES.ENTER_PLAN_MODE:
      return 'Enter plan mode'
    default:
      return undefined
  }
}
