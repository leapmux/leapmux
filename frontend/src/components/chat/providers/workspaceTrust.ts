import type { SendPermissionOption } from '../controls/PermissionDecisionActions'
import type { PermissionOption } from '../model/controlPrompt'
import type { ControlResponseDeriver } from '../persistedControlResponse'
import type { ControlExtractionInput, ExtractedControlRequest } from './capabilities'
import { ACP_PERMISSION_KIND } from '~/generated/contracts/acp-protocol'
import { WORKSPACE_TRUST_CONTROL_TYPE, WORKSPACE_TRUST_DECISION, WORKSPACE_TRUST_FIELD } from '~/generated/contracts/workspace-trust'
import { pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { buildControlResponseEnvelope } from '~/utils/controlResponse'
import { sendResponse } from '../controls/types'
import { KIND_REJECT_ONCE } from '../model/controlPrompt'
import { label } from '../persistedControlResponse'

/**
 * The workspace-trust question that LeapMux asks for an agent.
 *
 * Some agents load part of a project's configuration only from a folder they trust,
 * but ask nothing in the mode that LeapMux runs them in. The Worker then asks
 * (`providerkit.WorkspaceTrust`): the question states the folder and what the trust
 * controls, and the saved answer states `trust` or `reject`
 * (`contracts/workspace-trust.json`). This module draws that question and that answer
 * for every plugin whose agent the Worker asks for.
 *
 * The question is LeapMux's own shape, so it is neutral. An agent that raises its own
 * trust request reads that request in its own plugin, and may draw it with
 * {@link workspaceTrustPermission}.
 */

/** One workspace-trust question: the folder, and what the trust controls in words the reader reads. */
export interface WorkspaceTrustQuestion {
  path: string
  configuration: readonly string[]
}

/** The option ids of the two answers, which no agent's own option ids reuse. */
export const WORKSPACE_TRUST_OPTION = {
  Trust: 'workspace-trust',
  Reject: 'workspace-reject',
} as const

/** The two answers, as the decision row draws them and as a saved answer reads. */
export const WORKSPACE_TRUST_OPTIONS: readonly PermissionOption[] = [
  { optionId: WORKSPACE_TRUST_OPTION.Trust, kind: ACP_PERMISSION_KIND.AllowOnce, name: 'Trust this folder' },
  { optionId: WORKSPACE_TRUST_OPTION.Reject, kind: KIND_REJECT_ONCE, name: 'Do not trust' },
]

/** The question that one stored payload carries, or undefined for any other payload. */
export function workspaceTrustQuestion(payload: Record<string, unknown> | undefined): WorkspaceTrustQuestion | undefined {
  if (pickString(payload, WORKSPACE_TRUST_FIELD.Type) !== WORKSPACE_TRUST_CONTROL_TYPE.Question)
    return undefined
  return {
    path: pickString(payload, WORKSPACE_TRUST_FIELD.Path),
    configuration: stringArray(payload?.[WORKSPACE_TRUST_FIELD.Configuration]).filter(Boolean),
  }
}

/** Join the configuration words into one phrase: "MCP servers", "A and B", "A, B and C". */
function configurationPhrase(configuration: readonly string[]): string {
  if (configuration.length === 0)
    return 'the configuration'
  if (configuration.length === 1)
    return `the ${configuration[0]}`
  return `the ${configuration.slice(0, -1).join(', ')} and ${configuration.at(-1)}`
}

/**
 * The decision row of one workspace-trust question. `agentName` is the agent that
 * keeps the trusted list, as the reader knows it.
 */
export function workspaceTrustPermission(
  question: WorkspaceTrustQuestion,
  agentName: string,
  options: readonly PermissionOption[] = WORKSPACE_TRUST_OPTIONS,
): ExtractedControlRequest {
  return {
    kind: 'permission',
    permission: {
      title: question.path ? `Trust the folder ${question.path}` : 'Trust this folder',
      reason: `${agentName} loads ${configurationPhrase(question.configuration)} of a project only from a folder that you trust, and it keeps your answer for the folder.`,
      options: [...options],
    },
  }
}

/** Draw the workspace-trust question, and hand every other payload to `extract`. */
export function withWorkspaceTrustControl(
  extract: (input: ControlExtractionInput) => ExtractedControlRequest | null,
  agentName: string,
): (input: ControlExtractionInput) => ExtractedControlRequest | null {
  return (input) => {
    const question = workspaceTrustQuestion(input.payload)
    return question ? workspaceTrustPermission(question, agentName) : extract(input)
  }
}

/**
 * Send an answer to the workspace-trust question as the neutral decision, and hand
 * every other option to `send`. The Worker turns the decision into the saved
 * `{decision}`.
 */
export function withWorkspaceTrustOptions(send: SendPermissionOption): SendPermissionOption {
  return (onRespond, requestId, optionId) => {
    switch (optionId) {
      case WORKSPACE_TRUST_OPTION.Trust:
        return sendResponse(onRespond, buildControlResponseEnvelope(requestId, { behavior: 'allow' }))
      case WORKSPACE_TRUST_OPTION.Reject:
        return sendResponse(onRespond, buildControlResponseEnvelope(requestId, { behavior: 'deny' }))
      default:
        return send(onRespond, requestId, optionId)
    }
  }
}

/** Read a saved answer to the workspace-trust question, and hand every other answer to `display`. */
export function withWorkspaceTrustResponse(display: ControlResponseDeriver): ControlResponseDeriver {
  return (cr) => {
    if (!workspaceTrustQuestion(cr.request))
      return display(cr)
    const answer = pickObject(pickObject(cr.response, 'response'), 'response')
    switch (pickString(answer, WORKSPACE_TRUST_FIELD.Decision)) {
      case WORKSPACE_TRUST_DECISION.Trust:
        return label('Trusted this folder')
      case WORKSPACE_TRUST_DECISION.Reject:
        return label('Did not trust this folder')
      default:
        return null
    }
  }
}
