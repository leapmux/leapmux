import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { fastAgentACPPlugin } from './acpPlugin'
import { fastAgentArchiveSource, hasFastAgentArchiveSource } from './archiveSource'

export function fastAgentSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const source = fastAgentArchiveSource(parsed)
  if (!source) {
    return hasFastAgentArchiveSource(parsed)
      ? 'other'
      : fastAgentACPPlugin.transcript.spanRole?.(parsed) ?? 'other'
  }
  if (source.kind === 'request')
    return 'request'
  if (source.kind === 'result')
    return 'result'
  return 'none'
}

/**
 * Whether the row of one message draws from its span's request.
 *
 * An archived row never does: a request states its own call, and a result carries
 * the request that it answers in its own provenance. A live row asks the Agent Client
 * Protocol reader.
 */
export function fastAgentNeedsRequest(parsed: ResolvedMessageContent): boolean {
  if (hasFastAgentArchiveSource(parsed))
    return false
  return fastAgentACPPlugin.transcript.needsRequest?.(parsed) ?? false
}
