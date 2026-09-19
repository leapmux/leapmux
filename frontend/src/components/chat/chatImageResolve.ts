import type { MessageContextResolver, ResolvedMessage } from './messageContextResolver'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { createLogger } from '~/lib/logger'
import { imagesForIR } from './ir/derivations'
import { extractedRow } from './rowExtraction'
import { extractPreparedRow, prepareMessage } from './rowPreparation'

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
 * The images one message carries, in the order its provider read them.
 *
 * Read from the SAME row IR the chat row drew. That is the whole reason
 * `imageIndex` means anything: two walks of the same JSON would agree until one
 * of them learned a new block kind, and by then the tab would show a
 * different picture than the row the reader clicked.
 */
const logger = createLogger('chatImageResolve')

/** What a resolver adds to one message's own bytes. Every field is absent for an isolated read. */
export interface ImageExtractionSources {
  /** The message as the resolver currently holds it, with its supplemental content. */
  resolved?: ResolvedMessage
  /** The span's opener, which carries the file metadata a result alone does not. */
  request?: ReturnType<MessageContextResolver['request']>
  /**
   * The live to-do store. The transcript row reads it, so a resolver that left it
   * out read a DIFFERENT row than the one the reader clicked -- and the index of a
   * picture only means anything while the two rows agree.
   */
  todoById?: MessageContextResolver['todo']
}

export function messageToolResultImages(message: AgentChatMessage, sources: ImageExtractionSources = {}): ImageResultSource[] {
  // The extraction guards the PLUGIN call, and two unguarded plugin calls run before
  // it: `resolveMessage` inside the supplemental merge, and `classify`. A frame that
  // makes either one throw used to degrade to no images; without this it reaches the
  // caller's outer catch and the image tab shows a raw JavaScript error where the
  // picture belongs.
  try {
    // The resolver's own payload when it holds one, so the tab reads the row the
    // transcript drew. Preparation CLASSIFIES that payload, which is the correction
    // this carries: the tab used to classify the raw bytes and extract the merged
    // ones, so an ACP result wrapped in a native envelope was extracted as a row its
    // own category contradicted and its pictures were lost.
    const resolved = sources.resolved?.resolved
    const prepared = prepareMessage(message, ...(resolved === undefined ? [{}] : [{ resolved }]))
    // `role: 'result'` is deliberate and not the row's own place in its span: a
    // resolver that runs outside the render tree must read the FINISHED side, or a
    // provider that requires a completed call before it states its picture (Codex
    // states no status on an `imageView` item) resolves every image tab to nothing.
    const extraction = extractPreparedRow(prepared, {
      sides: { current: prepared.resolved, request: sources.request?.resolved, result: undefined, role: 'result' },
      ...(sources.todoById === undefined ? {} : { todoById: sources.todoById }),
    })
    return imagesForIR(extractedRow(extraction))
  }
  catch (err) {
    logger.warn('image extraction failed', { id: message.id, err })
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
    const { spanId, agentSessionId } = resolved.message
    const identity = { spanId, agentSessionId }
    const release = messages.retainSpan(identity)
    try {
      if (spanId && !messages.request(identity)) {
        try {
          await messages.loadRelated(resolved.message, resolved.original)
        }
        catch (error) {
          // A request adds file metadata. An unavailable request must not hide an available image.
          console.warn('Cannot load image request metadata', { spanId, error })
        }
      }
      const current = messages.current(resolved.message, resolved.original)
      const source = messageToolResultImages(current.message, {
        resolved: current,
        request: messages.request(identity),
        todoById: messages.todo,
      })[ref.imageIndex]
      if (!source)
        return { status: 'gone' }
      if (!source.data && !source.url && source.filePath)
        return { status: 'ready', source: await messages.fileImage(source.filePath, { reference: current.message.id }) }
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
