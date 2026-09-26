import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'

/** The Qoder tool names this extractor draws with a shell command. */
const QODER_SHELL_TOOLS = new Set(['Bash'])

/**
 * `Provider.extractControl` for Qoder CLI.
 *
 * A can_use_tool request states `tool_name`, `input` and its own display
 * fields. The extractor reads the neutral tool pair and the shell command; the
 * runtime's own option list stays unrendered, so the shared Allow/Deny pair is
 * the surface and the worker translates the answer into Qoder's
 * `{behavior, outcome}` object.
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
      options: [],
    },
  }
}
