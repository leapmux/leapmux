import type { ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'

/**
 * Counting session-goal transitions out of persisted messages.
 *
 * A leaf module on purpose: it imports only the generated proto types and the
 * browser's own decoder, so a `.test.ts` beside it runs under vitest in
 * milliseconds. `./subagentRegistry.ts` pulls in the Playwright fixtures and
 * cannot be imported from a unit test at all, which is why this does not live
 * there.
 */

/** The persisted-message fields the transition counter reads. */
export interface PersistedMessageContent {
  content: Uint8Array
  contentCompression: ContentCompression
}

/**
 * Count the session-goal transitions in a page of persisted messages.
 *
 * Two things it gets right, and both were wrong first.
 *
 * Every persisted message is zstd-compressed UNCONDITIONALLY (msgcodec.Compress
 * has no size threshold), so the bytes go through the same decoder the browser
 * uses. Reading them as text finds nothing and reports zero -- a silent wrong
 * answer indistinguishable from the feature being broken.
 *
 * And it counts TYPE TOKENS, not messages. Adjacent notifications fold into one
 * notification_thread row that carries each entry inside it, so counting rows
 * would report four transitions as one.
 */
export function countGoalTransitionsInMessages(messages: PersistedMessageContent[]): number {
  let count = 0
  for (const message of messages) {
    const body = decompressContentToString(message.content, message.contentCompression)
    count += body?.match(/"goal_(?:updated|cleared)"/g)?.length ?? 0
  }
  return count
}
