import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { CLINE_EVENT, CLINE_TOOL, CLINE_TOOL_PREFIX } from '~/generated/contracts/cline-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { clineCommandRequest } from './extractors/execute'
import { parseJSON } from './extractors/toolCommon'
import { clinePayload } from './protocol'
import { CLINE_SPAWN_WARNING } from './spawnWarning'
import { clineToolKind } from './toolKinds'
import { CLINE_TOOL_NAME } from './toolNames'

/**
 * Whether a call starts an agent that runs its tools without asking: a subagent, a
 * configured agent (`subagent_<name>_<hash>`), a new teammate, or a teammate's task
 * run.
 */
function startsAgent(toolName: string): boolean {
  return toolName === CLINE_TOOL.SpawnAgent
    || toolName.startsWith(CLINE_TOOL_PREFIX.ConfiguredAgent)
    || toolName === CLINE_TOOL_NAME.TeamSpawnTeammate
    || toolName === CLINE_TOOL_NAME.TeamRunTask
}

/**
 * `Provider.extractControl` for Cline.
 *
 * The worker publishes Cline's own `approval.requested` event as the control request:
 *
 *   {"event":"approval.requested","payload":{"approvalId":"...","toolCallId":"...",
 *    "toolName":"run_commands","inputJson":"{\"commands\":[\"ls\"]}", ...}}
 *
 * The approval of `switch_to_act_mode` is the plan approval: the model calls it after
 * it presented the plan in its answer, so the plan is the row above the banner. Every
 * other approval is a permission for the call it states, with the shared Allow and
 * Deny: Cline offers no option list. A question is a capability request, which
 * `askUserQuestion.isRequest` recognizes before this reader runs.
 */
export function clineExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const approval = clinePayload(input.payload, CLINE_EVENT.ApprovalRequested)
  if (!approval)
    return null
  const toolName = pickString(approval, 'toolName')
  if (toolName === CLINE_TOOL.SwitchToActMode)
    return { kind: 'plan' }
  const parsed = parseJSON(pickString(approval, 'inputJson'))
  const args = isObject(parsed) ? parsed : {}
  const command = clineToolKind(toolName) === 'execute' ? clineCommandRequest(args).command : ''
  return {
    kind: 'permission',
    permission: {
      title: toolName || 'Tool',
      ...(startsAgent(toolName) ? { reason: CLINE_SPAWN_WARNING } : {}),
      ...(command ? { command } : {}),
      input: args,
      options: [],
    },
  }
}
