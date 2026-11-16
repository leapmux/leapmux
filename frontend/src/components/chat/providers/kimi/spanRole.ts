import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { KIMI_TRANSCRIPT_TOOL } from '~/generated/contracts/kimi-protocol'
import { retainedRowIsFinal } from '../registry'
import { kimiTranscriptTool } from './transcript'

/**
 * A running native tool frame opens a span, and its finished frame closes it.
 * The completion column identifies a retained closing copy of a running frame.
 */
export function kimiSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const native = kimiTranscriptTool(parsed.parentObject)
  if (native) {
    return native.state === KIMI_TRANSCRIPT_TOOL.Running && !retainedRowIsFinal(parsed.completion)
      ? 'request'
      : 'result'
  }
  return 'other'
}

/** Results validate their earlier request's native identity for span layout. */
export function kimiNeedsRequest(parsed: ResolvedMessageContent): boolean {
  return kimiSpanRole(parsed) === 'result'
}
