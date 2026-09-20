import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { pickString } from '~/lib/jsonPick'
import { getToolName } from '~/utils/controlResponse'
import { codexRequestedPermissions, getCodexParams } from './controlResponse'

/** The tool name Codex gives its plan-mode prompt. */
const CODEX_PLAN_MODE_PROMPT = 'CodexPlanModePrompt'

/** Whether one control request is Codex's plan-mode prompt. */
export function isCodexPlanModePrompt(payload: Record<string, unknown>): boolean {
  return getToolName(payload) === CODEX_PLAN_MODE_PROMPT
}

/**
 * The approval methods Codex sends, and the operation each one identifies.
 *
 * `item/permissions/requestApproval` is absent on purpose: it asks for a SET of
 * permissions rather than one operation, and the set itself is the content.
 */
const CODEX_APPROVAL_TITLES: Record<string, string> = {
  'item/commandExecution/requestApproval': 'Command Execution',
  'item/fileChange/requestApproval': 'File Change',
}

const CODEX_PERMISSIONS_APPROVAL = 'item/permissions/requestApproval'

/** `Provider.extractControl` for Codex. */
export function codexExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  if (isCodexPlanModePrompt(payload))
    return { kind: 'plan' }
  const params = getCodexParams(payload)
  const method = pickString(payload, 'method')
  // `Object.hasOwn`, not a bare read: `method` comes straight off the wire, and a
  // value that spells an `Object.prototype` member answers with a function the
  // permission card would then draw as its own title.
  const title = Object.hasOwn(CODEX_APPROVAL_TITLES, method) ? CODEX_APPROVAL_TITLES[method] : undefined
  const reason = pickString(params, 'reason', undefined)
  const command = pickString(params, 'command', undefined)
  const workingDirectory = pickString(params, 'cwd', undefined)
  return {
    kind: 'permission',
    permission: {
      ...(title !== undefined ? { title } : {}),
      ...(reason !== undefined ? { reason } : {}),
      ...(command !== undefined ? { command } : {}),
      ...(workingDirectory !== undefined ? { workingDirectory } : {}),
      // A permissions approval draws the SET it asks for; every other method draws
      // nothing here, because its command and its reason already state the ask.
      input: method === CODEX_PERMISSIONS_APPROVAL ? codexRequestedPermissions(payload) : undefined,
      options: [],
    },
  }
}
