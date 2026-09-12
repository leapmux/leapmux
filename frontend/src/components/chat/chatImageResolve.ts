import type { MessageContextResolver, ResolvedMessage } from './messageContextResolver'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { parseMessageContent } from '~/lib/messageParser'
import { parsedMessageForRendering, pluginFor } from './providers/registry'

// Image tabs keep a message sequence and image index. The shared resolver supplies the bytes.

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

/**
 * The images one message carries, in the order its provider defines.
 *
 * Routed through `Provider.toolResultImages` -- the SAME function the chat row
 * rendered from. That is the whole reason `imageIndex` means anything: two
 * walks of the same JSON would agree until one of them learned a new block
 * kind, and by then the tab would be showing a different image than the row the
 * user clicked.
 */
export function messageToolResultImages(message: AgentChatMessage, resolved?: ResolvedMessage, request?: ReturnType<MessageContextResolver['request']>): ImageResultSource[] {
  try {
    const parsed = resolved?.parsed ?? parsedMessageForRendering(parseMessageContent(message), message.agentProvider)
    const plugin = pluginFor(message.agentProvider)
    return plugin?.toolResultImages?.({ parsed, spanType: message.spanType, request: request?.parsed }) ?? []
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
  ref: { seq: bigint, imageIndex: number },
  messages: MessageContextResolver | undefined,
): Promise<ChatImageResolution> {
  if (ref.seq <= 0n || ref.imageIndex < 0 || !Number.isInteger(ref.imageIndex))
    return { status: 'gone' }
  if (!messages)
    return { status: 'pending' }
  try {
    const resolved = await messages.message(ref.seq)
    if (!resolved)
      return { status: 'gone' }
    const spanId = resolved.message.spanId
    const release = messages.retainSpan(spanId)
    try {
      if (spanId && !messages.request(spanId)) {
        try {
          await messages.loadRelated(resolved.message, resolved.original)
        }
        catch (error) {
          // A request adds file metadata. An unavailable request must not hide an available image.
          console.warn('Cannot load image request metadata', { spanId, error })
        }
      }
      const current = messages.current(resolved.message, resolved.original)
      const source = messageToolResultImages(current.message, current, messages.request(spanId))[ref.imageIndex]
      if (!source)
        return { status: 'gone' }
      if (!source.data && !source.url && source.filePath)
        return { status: 'ready', source: await messages.fileImage(source.filePath, { reference: current.message.spanId || current.message.id }) }
      return { status: 'ready', source }
    }
    finally {
      release()
    }
  }
  catch (err) {
    return { status: 'error', message: err instanceof Error ? err.message : String(err) }
  }
}
