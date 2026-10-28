import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole, ToolSpanSide } from '~/lib/messageSpan'
import { fastAgentACPPlugin } from './acpPlugin'
import { fastAgentArchiveSource, hasFastAgentArchiveSource } from './archiveSource'

export function fastAgentSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const source = fastAgentArchiveSource(parsed)
  if (!source)
    return hasFastAgentArchiveSource(parsed) ? 'other' : fastAgentACPPlugin.transcript.spanRole?.(parsed) ?? 'other'
  if (source.kind === 'request')
    return 'request'
  if (source.kind === 'result')
    return 'result'
  return 'none'
}
export function fastAgentRelatedMessages(parsed: ResolvedMessageContent): readonly ToolSpanSide[] {
  const source = fastAgentArchiveSource(parsed)
  if (!source)
    return hasFastAgentArchiveSource(parsed) ? [] : fastAgentACPPlugin.transcript.relatedMessages?.(parsed) ?? []
  if (source.kind === 'request')
    return ['result']
  if (source.kind === 'result')
    return ['request']
  return []
}
