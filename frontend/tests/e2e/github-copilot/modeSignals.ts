import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { nativeModelInstructionText, nativeModelLastUserText } from '../helpers/nativeScenario'

/**
 * Copilot's own statements of the session mode in a model request.
 *
 * The tool list cannot prove the mode after the session leaves Plan. Copilot CLI 1.0.103 and later keep the tool list
 * and the system message of a model request append-only:
 *
 * - A switch to Plan adds `exit_plan_mode` and `task_complete`. A switch back keeps both tools.
 * - Each mode change appends a notice to the system message. No later change removes the notice.
 * - The runtime refuses a call to a kept tool outside its mode with {@link COPILOT_KEPT_MODE_TOOL_RESULT}.
 */

/** Copilot puts this prefix before the text of each user turn that it handles in Plan mode. */
const PLAN_TURN_PREFIX = /^\[\[PLAN\]\] /m

/** One mode notice. The first group captures its body. */
const MODE_NOTICE = /<mode_changed_notice>([\s\S]*?)<\/mode_changed_notice>/g

/** The first sentence of the notice that Copilot appends when the session leaves Plan. */
export const COPILOT_PLAN_ENDED_NOTICE = 'Plan mode is no longer active.'

/** The result that Copilot CLI 1.0.103 and later return for a call to a kept mode tool outside its mode. */
export const COPILOT_KEPT_MODE_TOOL_RESULT = 'This tool is not applicable in the current mode.'

/**
 * Whether Copilot handled the last user turn of a request in Plan mode.
 * The Plan instructions tell the model to read the prefix, and the prefix is the one mode statement that each turn carries.
 */
export function isCopilotPlanTurn(request: MockModelRequestRecord): boolean {
  return PLAN_TURN_PREFIX.test(nativeModelLastUserText(request))
}

/**
 * Read the body of the last mode notice in a request.
 * Copilot keeps each earlier notice, so only the last one states the current mode.
 * Return undefined when the session never changed its mode.
 */
export function latestCopilotModeNotice(request: MockModelRequestRecord): string | undefined {
  return [...nativeModelInstructionText(request).matchAll(MODE_NOTICE)].at(-1)?.[1]?.trim()
}
