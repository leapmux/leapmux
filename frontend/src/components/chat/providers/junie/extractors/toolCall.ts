import type { ACPToolCallAdapter } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { JUNIE_SPAWN_FIELD, JUNIE_TOOL } from '~/generated/contracts/junie-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { acpRemapFacts, acpSpecFor } from '../../acp/extractors/toolCall'

/**
 * Junie's reading of one ACP tool call.
 *
 * Junie stamps no tool name on an ACP tool call: `title` is the call's display
 * label ("ls" for a bash command). The `agent` field of `rawInput` is the one
 * identifier that only `spawn_subagent` carries, so this adapter upgrades that
 * call to an `agent` row and leaves every other call to the shared builder.
 * The tool name is the fallback for a call whose raw input this build does not
 * parse. A call that carries a `handle` is a continuation of a child that
 * already runs, so its request states the handle beside the prompt.
 */
export const junieToolCallAdapter: ACPToolCallAdapter = (facts, base) => {
  const agentType = pickString(facts.args, JUNIE_SPAWN_FIELD.Agent)
  const handle = pickString(facts.args, JUNIE_SPAWN_FIELD.Handle)
  const title = pickString(facts.tool, 'title')
  if ((agentType === undefined || agentType === '') && title !== JUNIE_TOOL.SpawnSubagent)
    return base()
  const description = pickString(facts.args, 'name') ?? title ?? JUNIE_TOOL.SpawnSubagent
  const prompt = pickString(facts.args, 'extraContext') ?? ''
  const rawInput = isObject(facts.tool[ACP_SUPPLEMENT_REQUEST.RawInput])
    ? { ...(facts.tool[ACP_SUPPLEMENT_REQUEST.RawInput] as Record<string, unknown>), description, prompt }
    : { description, prompt }
  const remapped = acpRemapFacts(facts, {
    tool: { ...facts.tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: rawInput },
    kind: 'agent',
  })
  const spec = acpSpecFor(remapped, 'agent')
  if (handle !== undefined && handle !== '' && spec.request.agentType === undefined) {
    return { ...spec, request: { ...spec.request, agentType } }
  }
  return spec
}
