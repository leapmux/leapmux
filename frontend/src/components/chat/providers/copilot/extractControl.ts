import type { ControlQuestion } from '../../model/question'
import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { pickString } from '~/lib/jsonPick'
import { copilotPermission, copilotPermissionOptions } from './permissionOptions'
import { copilotEvent } from './protocol'

/** The title and the detail a permission request shows. */
export function copilotPermissionSource(payload: Record<string, unknown>) {
  const request = copilotPermission(payload)
  const kind = pickString(request, 'kind')
  const title = pickString(request, 'intention')
    || pickString(request, 'toolTitle')
    || pickString(request, 'toolName')
    || kind
  // The command is optional on the model, so it stays ABSENT for every other kind.
  const command = kind === 'shell' ? pickString(request, 'fullCommandText', undefined) : undefined
  return {
    title,
    input: request,
    ...(command !== undefined ? { command } : {}),
  }
}

/** True for the question request, which the shared question control answers. */
export function copilotIsQuestion(payload: Record<string, unknown>): boolean {
  return copilotEvent(payload)?.type === COPILOT_EVENT.UserInputRequested
}

/**
 * The question one `user_input.requested` carries.
 *
 * The runtime states its choices as plain strings, and `allowFreeform` states whether
 * it accepts an answer that is not one of them. The empty answer is the shortest such
 * answer, so the control offers it exactly where the runtime takes one. A request that
 * states no flag wants one of its choices, and the submit then waits for one.
 */
export function copilotQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const data = copilotEvent(payload)?.data
  if (!data)
    return []
  const choices = Array.isArray(data.choices) ? data.choices.filter((choice): choice is string => typeof choice === 'string') : []
  return [{
    question: pickString(data, 'question'),
    options: choices.map(choice => ({ label: choice })),
    allowEmpty: data.allowFreeform === true,
  }]
}

/** `Provider.extractControl` for GitHub Copilot. */
export function copilotExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  // A QUESTION never reaches here: `askUserQuestion.isRequest` is the one recognizer,
  // and the control surface answers it before any provider's reader runs.
  const event = copilotEvent(payload)
  if (event?.type === COPILOT_EVENT.ExitPlanModeRequested) {
    // Copilot sends the WHOLE plan in its approval request, where every other
    // provider sends an approval that identifies a plan the transcript already holds.
    return { kind: 'plan', text: pickString(event.data, 'planContent') || pickString(event.data, 'summary') }
  }
  return {
    kind: 'permission',
    permission: { ...copilotPermissionSource(payload), options: copilotPermissionOptions(payload) },
  }
}
