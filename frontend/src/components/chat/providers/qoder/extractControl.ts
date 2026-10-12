import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { KIND_ALLOW_ALWAYS, KIND_ALLOW_ONCE, KIND_REJECT_ONCE } from '../../model/controlPrompt'

/** The Qoder tool names this extractor draws with a shell command. */
const QODER_SHELL_TOOLS = new Set(['Bash'])

/**
 * The scope answers Qoder's runtime accepts on an allow, which its own reader
 * maps onto `permissionScope: "session"` (kept for the session) and
 * `permissionScope: "persist"` (saved to the project). LeapMux offers the
 * session tier alone: the reader derives the session rule itself, so nothing
 * outlives the private session that granted it.
 */
const QODER_PERMISSION_OPTIONS: PermissionOption[] = [
  { optionId: 'once', kind: KIND_ALLOW_ONCE, name: 'Allow once' },
  { optionId: 'session', kind: KIND_ALLOW_ALWAYS, name: 'Allow for this session' },
  { optionId: 'deny', kind: KIND_REJECT_ONCE, name: 'Deny' },
]

/**
 * `Provider.extractControl` for Qoder CLI.
 *
 * A can_use_tool request states `tool_name`, `input` and its own display
 * fields. The extractor reads the neutral tool pair, the shell command, and the
 * scope answers the runtime accepts; the worker translates the chosen answer
 * into Qoder's `{behavior, permissionScope}` object.
 */
export function qoderExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  const toolName = getToolName(payload)
  if (toolName === 'ExitPlanMode')
    return { kind: 'plan' }
  const toolInput = getToolInput(payload)
  const command = QODER_SHELL_TOOLS.has(toolName)
    ? pickString(toolInput, 'command', undefined)
    : undefined
  return {
    kind: 'permission',
    permission: {
      title: toolName,
      input: toolInput,
      ...(command !== undefined ? { command } : {}),
      options: QODER_PERMISSION_OPTIONS,
    },
  }
}
