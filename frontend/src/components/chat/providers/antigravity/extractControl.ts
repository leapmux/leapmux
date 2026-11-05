import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import type { PermissionOption } from '~/components/chat/model/controlPrompt'
import { KIND_REJECT_ONCE } from '~/components/chat/model/controlPrompt'
import { ACP_PERMISSION_KIND } from '~/generated/contracts/acp-protocol'
import { ANTIGRAVITY_CONTROL_PAYLOAD, ANTIGRAVITY_CONTROL_TOOL, ANTIGRAVITY_PERMISSION_ACTION } from '~/generated/contracts/antigravity-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { getToolName } from '~/utils/controlResponse'

export const ANTIGRAVITY_PERMISSION_OPTIONS: readonly PermissionOption[] = [
  { optionId: 'allow', kind: ACP_PERMISSION_KIND.AllowOnce, name: 'Allow once' },
  { optionId: 'reject', kind: KIND_REJECT_ONCE, name: 'Reject' },
]

const PERMISSION_TITLES: ReadonlyMap<string, string> = new Map([
  [ANTIGRAVITY_PERMISSION_ACTION.Command, 'Run command'],
  [ANTIGRAVITY_PERMISSION_ACTION.ReadFile, 'Read file'],
  [ANTIGRAVITY_PERMISSION_ACTION.WriteFile, 'Write file'],
  [ANTIGRAVITY_PERMISSION_ACTION.Mcp, 'MCP tool'],
])

export function antigravityExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  if (getToolName(payload) === ANTIGRAVITY_CONTROL_TOOL.PlanReview) {
    const text = pickString(pickObject(pickObject(payload, 'request'), 'input'), ANTIGRAVITY_CONTROL_PAYLOAD.PlanContent)
    return {
      kind: 'plan',
      ...(text
        ? { text }
        : {}),
    }
  }
  const interaction = pickObject(payload, 'requestedInteraction')
  const permission = pickObject(interaction, 'permission')
  if (permission) {
    const resource = pickObject(permission, 'resource')
    const action = pickString(resource, 'action')
    return {
      kind: 'permission',
      permission: {
        title: pickString(permission, 'actionDescription') || PERMISSION_TITLES.get(action) || 'Permission',
        input: {
          action,
          target: pickString(resource, 'target'),
        },
        ...(action === ANTIGRAVITY_PERMISSION_ACTION.Command
          ? {
              command: pickString(resource, 'target'),
            }
          : {}),
        options: [...ANTIGRAVITY_PERMISSION_OPTIONS],
      },
    }
  }
  if (interaction && !pickObject(interaction, 'askQuestion')) {
    return {
      kind: 'permission',
      permission: {
        title: 'Antigravity interaction',
        input: { interaction },
        options: [...ANTIGRAVITY_PERMISSION_OPTIONS],
      },
    }
  }
  return null
}
