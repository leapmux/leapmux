import { FASTAGENT_ARCHIVE, FASTAGENT_TOOL } from '~/generated/contracts/fastagent-protocol'
import { isObject } from '~/lib/jsonPick'

/** Read the provenance supplied by this provider's root archive reader. */
export function fastAgentArchiveTool(tool: Record<string, unknown>, sessionId?: string): string | undefined {
  const meta = tool._meta
  const archive = isObject(meta) ? meta[FASTAGENT_ARCHIVE.Namespace] : undefined
  if (!isObject(archive))
    return undefined
  const owner = archive[FASTAGENT_ARCHIVE.SessionID]
  const file = archive[FASTAGENT_ARCHIVE.HistoryFile]
  const timestamp = archive[FASTAGENT_ARCHIVE.Timestamp]
  const callId = archive[FASTAGENT_ARCHIVE.CallID]
  const name = archive[FASTAGENT_ARCHIVE.ToolName]
  if (typeof owner !== 'string' || !owner || (sessionId !== undefined && owner !== sessionId)
    || typeof file !== 'string' || !/^history_[^/\\]+\.json$/u.test(file)
    || typeof timestamp !== 'string' || !Number.isFinite(Date.parse(timestamp))
    || typeof callId !== 'string' || !callId
    || (name !== FASTAGENT_TOOL.Bash && name !== FASTAGENT_TOOL.Process)) {
    return undefined
  }
  return name
}
