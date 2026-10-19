import type { NotificationEntry } from '../../../model/notification'
import { CODEBUDDY_FRAME_KIND, CODEBUDDY_SYSTEM_SUBTYPE } from '~/generated/contracts/codebuddy-protocol'
import { pickString } from '~/lib/jsonPick'

/** The level of an `informational` line that warns the reader. */
const INFORMATIONAL_WARNING_LEVEL = 'warning'

/** The server names of one list field of an `mcp_status` finish line. A malformed entry is skipped. */
function serverNames(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((name): name is string => typeof name === 'string' && name.trim() !== '') : []
}

/** One sentence for a list of MCP servers, in the singular or the plural form. */
function serverSentence(names: readonly string[], singular: string, plural: string): string {
  return `${names.length === 1 ? singular : plural}: ${names.join(', ')}`
}

/**
 * The entries of one CodeBuddy Code `system` or `error` line that the reader needs.
 *
 * - `system` `mcp_status` states each MCP server that failed to start or did not start in time.
 *   The worker stores only the finished report.
 * - `system` `informational` states its text, with its level when the line warns.
 * - `error` states the message of a failed request. CodeBuddy Code writes one when its executor fails, and an error
 *   with no message still states that an error occurred.
 *
 * Every other line holds no entry.
 */
export function codebuddyNotificationEntry(m: Record<string, unknown>): NotificationEntry[] {
  const type = pickString(m, 'type')
  if (type === CODEBUDDY_FRAME_KIND.Error) {
    const message = pickString(m, 'error').trim()
    return [{ kind: 'text', text: message === '' ? 'Error' : `Error: ${message}` }]
  }
  if (type !== CODEBUDDY_FRAME_KIND.System)
    return []
  switch (pickString(m, 'subtype')) {
    case CODEBUDDY_SYSTEM_SUBTYPE.McpStatus: {
      const failed = serverNames(m.failed)
      const timedOut = serverNames(m.timed_out)
      const entries: NotificationEntry[] = []
      if (failed.length > 0)
        entries.push({ kind: 'text', text: serverSentence(failed, 'Failed to start MCP server', 'Failed to start MCP servers') })
      if (timedOut.length > 0)
        entries.push({ kind: 'text', text: serverSentence(timedOut, 'MCP server did not start in time', 'MCP servers did not start in time') })
      return entries
    }
    case CODEBUDDY_SYSTEM_SUBTYPE.Informational: {
      const content = pickString(m, 'content').trim()
      if (content === '')
        return []
      return [{ kind: 'text', text: pickString(m, 'level') === INFORMATIONAL_WARNING_LEVEL ? `Warning: ${content}` : content }]
    }
    default:
      return []
  }
}
