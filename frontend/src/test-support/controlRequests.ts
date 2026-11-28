import type { ControlPayloadFault, ControlPrompt, PermissionOption } from '~/components/chat/model/controlPrompt'
import type { ControlEditorRole } from '~/components/chat/model/controlPromptWire'
import type { ControlRequest } from '~/stores/control.store'
import { screen, within } from '@solidjs/testing-library'
import { KIND_REJECT_ONCE } from '~/components/chat/model/controlPrompt'
import { ACP_PERMISSION_KIND } from '~/generated/contracts/acp-protocol'
import { CONTROL_ANSWER_OPTION } from '~/generated/contracts/control-answer'

/** LeapMux's own Allow and Deny (`agent.AllowDenyOptions` in Go), with the ids of the contract. */
export const ALLOW_DENY_OPTIONS: readonly PermissionOption[] = [
  { optionId: CONTROL_ANSWER_OPTION.Allow, kind: ACP_PERMISSION_KIND.AllowOnce },
  { optionId: CONTROL_ANSWER_OPTION.Deny, kind: KIND_REJECT_ONCE },
]

/**
 * The prompt of a request that the Worker's provider does not recognize but can still
 * answer: LeapMux's own Allow and Deny (`agent.GenericPermissionPrompt` in Go).
 */
export const GENERIC_PERMISSION_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    options: [...ALLOW_DENY_OPTIONS],
  },
}

/**
 * The editor role that the Worker states for a prompt that states none of its own
 * (`agent.WithEditorPurpose` in Go): a question takes an answer, an elicitation and a
 * dialog take no text, and anything else takes feedback.
 */
function defaultEditorRole(prompt: ControlPrompt): ControlEditorRole {
  switch (prompt.kind) {
    case 'question':
      return 'answer'
    case 'elicitation':
    case 'dialog':
      return 'none'
    default:
      return 'feedback'
  }
}

/**
 * One control request as the store holds it. A test states what it reads, and the
 * rest takes what the Worker sends for a request with no payload: the generic
 * permission prompt, and the editor role of the prompt.
 */
export function testControlRequest(fields: Pick<ControlRequest, 'agentId' | 'requestId'> & Partial<ControlRequest>): ControlRequest {
  const prompt = fields.prompt ?? GENERIC_PERMISSION_PROMPT
  return {
    payload: {},
    editorRole: defaultEditorRole(prompt),
    ...fields,
    prompt,
  }
}

/**
 * The bytes that arrive with each payload fault, and the payload that the browser
 * decodes from them (`handleControlRequest`). Only an `unrecognized` request is a JSON
 * object, so only its payload holds what arrived: a provider plugin could read that
 * object, and the request must still offer no answer.
 */
export const FAULTED_CONTROL_PAYLOADS: Readonly<Record<ControlPayloadFault, { bytes: string, payload: Record<string, unknown> }>> = {
  'malformed': { bytes: '{not json', payload: {} },
  'not-an-object': { bytes: '[1,2]', payload: {} },
  'unrecognized': {
    bytes: '{"method":"session/unknown","id":7}',
    payload: { method: 'session/unknown', id: 7 },
  },
}

/** Every payload fault, so a test that covers each fault also covers a new one. */
export const CONTROL_PAYLOAD_FAULTS = Object.keys(FAULTED_CONTROL_PAYLOADS) as ControlPayloadFault[]

/**
 * One request whose payload has `fault`, as `handleControlRequest` keeps it: the
 * decoded payload, the bytes that arrived, and no editor, which the Worker states for
 * every such request. A test states the rest, an editor role included.
 */
export function testFaultedControlRequest(
  fault: ControlPayloadFault,
  fields: Pick<ControlRequest, 'agentId' | 'requestId'> & Partial<ControlRequest>,
): ControlRequest {
  const { bytes, payload } = FAULTED_CONTROL_PAYLOADS[fault]
  return testControlRequest({
    editorRole: 'none',
    payload: structuredClone(payload),
    originalPayload: new TextEncoder().encode(bytes),
    ...fields,
    payloadFault: fault,
  })
}

/**
 * Reading a control-request banner's pill groups, for the suites that drive a
 * permission or plan-approval banner.
 *
 * Both groups are written by
 * `~/components/chat/controls/ControlPillGroups`. That component owns the
 * "Permissions" name. Each CALLER owns its own allow-choice name, so the
 * locator here takes the name as an argument:
 *
 * - "Allow scope" — `~/components/chat/controls/PermissionDecisionActions`.
 * - "Allow as" — `~/components/chat/controls/ElicitationControl`, for the accept
 *   choices of an MCP tool approval.
 *
 * Each of those spells its name ONCE, so a rename there reaches every site that
 * renders the group. It does NOT reach this file: the union below is a separate
 * spelling, and a rename must update it too.
 */

/** The permission preset group (Unchanged / Smart / Bypass). */
export function permissionPillGroup() {
  return within(screen.getByRole('radiogroup', { name: 'Permissions' }))
}

/** An allow-choice group, identified by its provider-facing label. */
export function allowChoicePillGroup(label: 'Allow scope' | 'Allow as' = 'Allow scope') {
  return within(screen.getByRole('radiogroup', { name: label }))
}
