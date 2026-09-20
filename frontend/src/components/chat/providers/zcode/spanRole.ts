import type {} from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { ZCODE_EVENT, ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { zcodeControlPlanText } from './extractors/plan'
import { zcodeEnvelope, zcodeExtractTool, zcodeToolSpanRole } from './extractors/toolCommon'

const ZCODE_REQUESTS_WITH_TITLES = new Set<string>([
  ZCODE_TOOL.Bash,
  ZCODE_TOOL.Read,
  ZCODE_TOOL.Write,
  ZCODE_TOOL.Edit,
  ZCODE_TOOL.Glob,
  ZCODE_TOOL.Grep,
  ZCODE_TOOL.TodoWrite,
  ZCODE_TOOL.Agent,
  ZCODE_TOOL.WebFetch,
])

/**
 * ZCode span role. The `tool.updated` KIND discriminates the request from the result,
 * because both halves arrive as the same event type -- a content-block scan would
 * bucket every one of them the same way.
 */
export function zcodeSpanRole(parsed: ParsedMessageContent): ToolSpanRole {
  if (zcodeControlPlanText(parsed.parentObject) !== null)
    return 'request'
  const envelope = zcodeEnvelope(parsed.parentObject)
  if (!envelope || envelope.type !== ZCODE_EVENT.ToolUpdated)
    return 'other'
  return zcodeToolSpanRole(pickString(envelope.payload, 'kind'), parsed)
}

export function zcodeRelatedMessages(parsed: ParsedMessageContent) {
  if (zcodeControlPlanText(parsed.parentObject) !== null)
    return []
  const role = zcodeSpanRole(parsed)
  if (role === 'result')
    return ['request'] as const
  const tool = zcodeExtractTool(parsed.parentObject)
  return role === 'request' && (tool?.toolName === ZCODE_TOOL.Agent || tool?.toolName === ZCODE_TOOL.TodoWrite || Object.keys(tool?.input ?? {}).length === 0 || !ZCODE_REQUESTS_WITH_TITLES.has(tool?.toolName ?? ''))
    ? ['result'] as const
    : []
}
