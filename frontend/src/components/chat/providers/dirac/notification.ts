import type { NotificationEntry } from '../../model/notification'
import type { ACPNativeNotifications } from '../acp/notification'
import { ACP_UPDATE } from '~/generated/contracts/acp-protocol'
import { DIRAC_NOTICE } from '~/generated/contracts/dirac-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'

/**
 * Read one stored Dirac notice chunk. The worker stores a chunk whose whole
 * text equals one of Dirac's notices; the reader turns each into the row the
 * notice states.
 */
export function diracNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  const text = pickString(pickObject(message, 'content'), 'text')
  if (message.sessionUpdate !== ACP_UPDATE.AgentMessageChunk || text === '')
    return []
  switch (text) {
    case DIRAC_NOTICE.RetryingApiRequest:
      return [
        { kind: 'status', text: 'Retrying the model request' },
      ]
    case DIRAC_NOTICE.HookCancelled:
      return [
        { kind: 'hook', outcome: 'blocked', detail: 'cancelled by user' },
      ]
    case DIRAC_NOTICE.ShellIntegrationUnavailable:
      return [
        { kind: 'status', text },
      ]
    case DIRAC_NOTICE.ShellIntegrationSuggestion:
      return [
        { kind: 'status', text },
      ]
    default:
      return []
  }
}

export const diracNativeNotifications: ACPNativeNotifications = {
  isMember: message => diracNotificationEntry(message).length > 0,
  entries: diracNotificationEntry,
}
