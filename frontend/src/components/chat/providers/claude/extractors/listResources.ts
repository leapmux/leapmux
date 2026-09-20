import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { ListRequest } from '../../../model/tools/list'
import type { ClaudeToolRow } from './toolCommon'
import { isObject, pickString } from '~/lib/jsonPick'
import { unparsedResult } from '../../../model/toolCall'
import { claudeToolFailureResult } from './failure'

/**
 * The list pair of an MCP resource listing: the server's resources, one entry
 * each. A listing this build cannot read stays unparsed with the reason below.
 *
 * A FAILED listing is a different statement, and the failure rung leads for that
 * reason. It carries no resource array either, so it fell to the unparsed rung, which
 * states that the call completed and contradicts the row's own failed status.
 */
export function claudeListResourcesSpec(request: ListRequest, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'list'> {
  if (!result)
    return { kind: 'list', request }
  const failure = claudeToolFailureResult(result)
  if (failure)
    return { kind: 'list', request, result: failure }
  const resources = Array.isArray(result.toolUseResult?.resources) ? result.toolUseResult.resources : []
  const entries = resources.flatMap((resource) => {
    if (!isObject(resource))
      return []
    const uri = pickString(resource, 'uri')
    const detail = pickString(resource, 'name')
    // The detail rides only when the server named the resource; a blank name
    // states nothing a reader could draw.
    return uri ? [{ path: uri, ...(detail ? { detail } : {}) }] : []
  })
  // A server that answered with no readable resource list stated something this
  // build cannot read as files; the raw text keeps it.
  if (entries.length === 0 && resources.length === 0)
    return { kind: 'list', request, result: unparsedResult(result.resultContent) }
  return { kind: 'list', request, result: { entries } }
}
