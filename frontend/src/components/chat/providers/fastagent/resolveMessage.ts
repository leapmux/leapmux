import type { ParsedMessageContent } from '~/lib/messageParser'
import { fastAgentACPPlugin } from './acpPlugin'
import { hasFastAgentArchiveSource } from './archiveSource'

export function resolveFastAgentMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  if (hasFastAgentArchiveSource(parsed))
    return parsed.parentObject
  return fastAgentACPPlugin.transcript.resolveMessage?.(parsed) ?? parsed.parentObject
}
