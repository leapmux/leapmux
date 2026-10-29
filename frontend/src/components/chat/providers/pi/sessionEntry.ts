import { PI_EVENT } from '~/generated/contracts/pi-protocol'
import { pickObject } from '~/lib/jsonPick'

/** Read an entry only from its native session envelope. */
export function piSessionEntry(message: Record<string, unknown>): Record<string, unknown> | undefined {
  return message.type === PI_EVENT.EntryAppended
    ? pickObject(message, 'entry') ?? undefined
    : undefined
}
