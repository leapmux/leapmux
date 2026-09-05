import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { parseMessageContent } from '~/lib/messageParser'
import { pluginFor } from './providers/registry'

// ---------------------------------------------------------------------------
// Resolving an IMAGE tab's reference back to pixels
//
// An IMAGE tab stores `(agentId, seq, imageIndex)` and no bytes, so opening one
// means finding that message again. Which is the same problem the scroll rail's
// mark previews solve, and this follows their shape (see chatMarkPreview.ts):
// resolve from the loaded window when the message is there, else fetch the
// single message over E2EE, and keep the two outcomes distinguishable --
// "resolved, nothing there" is permanent, a failed RPC is not.
//
// It differs in two ways. A preview is a nicety, so the rail caches a '' and
// moves on; an image IS the tab, so a missing one has to say so.
//
// And there is no cache here, deliberately. The rail resolves a preview on every
// hover, whereas an IMAGE tab resolves once: `TileRenderer` keeps one pane per
// image tab of the tile mounted and only hides the inactive ones, so a tab
// switch re-fetches nothing. A module-global cache saves one RPC on a reopened
// tab, and the cost is that it holds whole `AgentChatMessage` protos --
// megabytes of base64 each -- for the life of the page. The rail caches a short
// string, so it has no such cost.
//
// Store DATA ops are injected, so this component-layer module never imports the
// DI'd chat store.
// ---------------------------------------------------------------------------

/** The outcome of resolving an image reference. */
export type ChatImageResolution
  = | { status: 'pending' }
  /** The image is available. */
    | { status: 'ready', source: ImageResultSource }
  /**
   * The message resolved and holds no image at that index -- it was deleted,
   * the seqs moved under the tab, or the provider now walks the blocks
   * differently. Permanent for this tab; retrying cannot help.
   */
    | { status: 'gone' }
  /** The lookup itself failed. Retryable. */
    | { status: 'error', message: string }

export interface ChatImageDeps {
  /** The loaded message at this seq, or undefined when it's outside the window. */
  getLoadedMessageBySeq: (agentId: string, seq: bigint) => AgentChatMessage | undefined
  /**
   * Fetch a single message by seq. Resolves `undefined` ONLY for a definitive
   * absence (no row at that seq); REJECTS on a transient RPC failure. The two
   * must stay distinguishable so a deleted message reads as `gone` while a
   * network blip stays retryable.
   */
  fetchMessageBySeq: (workerId: string, agentId: string, seq: bigint) => Promise<AgentChatMessage | undefined>
}

/**
 * The images one message carries, in the order its provider defines.
 *
 * Routed through `Provider.toolResultImages` -- the SAME function the chat row
 * rendered from. That is the whole reason `imageIndex` means anything: two
 * walks of the same JSON would agree until one of them learned a new block
 * kind, and by then the tab would be showing a different image than the row the
 * user clicked.
 */
export function messageToolResultImages(message: AgentChatMessage): ImageResultSource[] {
  try {
    const parsed = parseMessageContent(message)
    const plugin = pluginFor(message.agentProvider)
    return plugin?.toolResultImages?.(parsed.parentObject, message.spanType, undefined) ?? []
  }
  catch (err) {
    console.warn('image extraction failed', { id: message.id, err })
    return []
  }
}

/** Pick image N out of a message, or null when it has no such image. */
export function imageFromMessage(message: AgentChatMessage, imageIndex: number): ImageResultSource | null {
  return messageToolResultImages(message)[imageIndex] ?? null
}

/**
 * Resolve an image reference to a source, fetching the message when it is
 * outside the loaded window.
 *
 * A nonpositive sequence cannot identify a persisted message. Report it as gone.
 */
export async function resolveChatImage(
  ref: { workerId: string, agentId: string, seq: bigint, imageIndex: number },
  deps: ChatImageDeps,
): Promise<ChatImageResolution> {
  if (!ref.agentId || ref.seq <= 0n)
    return { status: 'gone' }

  const local = deps.getLoadedMessageBySeq(ref.agentId, ref.seq)
  if (local) {
    const source = imageFromMessage(local, ref.imageIndex)
    return source ? { status: 'ready', source } : { status: 'gone' }
  }

  try {
    const message = await deps.fetchMessageBySeq(ref.workerId, ref.agentId, ref.seq)
    if (!message)
      return { status: 'gone' }
    const source = imageFromMessage(message, ref.imageIndex)
    return source ? { status: 'ready', source } : { status: 'gone' }
  }
  catch (err) {
    return { status: 'error', message: err instanceof Error ? err.message : String(err) }
  }
}
