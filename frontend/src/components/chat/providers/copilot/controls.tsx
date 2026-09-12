import type { Component } from 'solid-js'
import type { ElicitationRequest } from '../../controls/elicitationForm'
import type { ActionsProps, ContentProps, Question } from '../../controls/types'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { pickString } from '~/lib/jsonPick'
import { PermissionDecisionActions } from '../../controls/PermissionDecisionActions'
import { PermissionRequestContent } from '../../controls/PermissionRequestContent'
import { MarkdownPlanLayout } from '../../widgets/MarkdownPlanLayout'
import { copilotPermission, copilotPermissionOptions, sendCopilotPermissionResponse } from './permissionOptions'
import { copilotEvent } from './protocol'

/** The title and the detail a permission request shows. */
function copilotPermissionSource(payload: Record<string, unknown>) {
  const request = copilotPermission(payload)
  const kind = pickString(request, 'kind')
  const title = pickString(request, 'intention')
    || pickString(request, 'toolTitle')
    || pickString(request, 'toolName')
    || kind
  return {
    title,
    input: request,
    command: kind === 'shell' ? pickString(request, 'fullCommandText', undefined) : undefined,
  }
}

export const CopilotControlContent: Component<ContentProps> = (props) => {
  const event = () => copilotEvent(props.request.payload)
  const plan = () => {
    const current = event()
    return current?.type === COPILOT_EVENT.ExitPlanModeRequested ? current.data : undefined
  }
  return (
    <>
      {plan()
        ? (
            <MarkdownPlanLayout
              toolName={COPILOT_EVENT.ExitPlanModeRequested}
              title="Proposed Plan"
              planText={pickString(plan(), 'planContent') || pickString(plan(), 'summary')}
              context={undefined}
            />
          )
        : <PermissionRequestContent request={props.request} source={copilotPermissionSource(props.request.payload)} />}
    </>
  )
}

export const CopilotControlActions: Component<ActionsProps> = props => (
  <PermissionDecisionActions {...props} options={copilotPermissionOptions} send={sendCopilotPermissionResponse} />
)

/** True for the question request, which the shared question control answers. */
export function copilotIsQuestion(payload: Record<string, unknown>): boolean {
  return copilotEvent(payload)?.type === COPILOT_EVENT.UserInputRequested
}

/**
 * The question one `user_input.requested` carries.
 *
 * The runtime states its choices as plain strings, and `allowFreeform` states whether
 * a typed answer is accepted. An empty answer is valid, so the control offers one.
 */
export function copilotQuestions(payload: Record<string, unknown>): Question[] {
  const data = copilotEvent(payload)?.data
  if (!data)
    return []
  const choices = Array.isArray(data.choices) ? data.choices.filter((choice): choice is string => typeof choice === 'string') : []
  return [{
    question: pickString(data, 'question'),
    options: choices.map(choice => ({ label: choice })),
    allowEmpty: true,
  }]
}

/** The elicitation form one `elicitation.requested` carries. */
export function copilotElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  const event = copilotEvent(payload)
  if (!event || event.type !== COPILOT_EVENT.ElicitationRequested)
    return undefined
  const data = event.data
  return {
    mode: pickString(data, 'mode', 'form'),
    message: pickString(data, 'message', ''),
    server: pickString(data, 'elicitationSource', ''),
    schema: data.requestedSchema,
    url: pickString(data, 'url', ''),
    title: '',
    description: '',
  }
}
