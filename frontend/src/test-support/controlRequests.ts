import type { ControlPrompt, PermissionOption } from '~/components/chat/model/controlPrompt'
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
 * The prompt of a request that the Worker's provider does not recognize: LeapMux's
 * own Allow and Deny (`agent.GenericPermissionPrompt` in Go).
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
