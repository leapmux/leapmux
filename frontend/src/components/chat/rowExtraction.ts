import type { MessageCompletion } from './assembledMessage'
import type { MessageCategory } from './messageClassifier'
import type { ChatRow } from './model/row'
import type { ResolvedMessageContent, RowExtractionInput } from './rowExtractionTypes'
import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import type { AgentProvider, MessageCompletion as ProtoMessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { isObject } from '~/lib/jsonPick'
import { createLogger } from '~/lib/logger'
import { protoJsonTodoToItem } from '~/stores/chatTodoStore'
import { messageCompletionFromProto, parseAssembledMessage } from './assembledMessage'
import { leapmuxUserRow } from './leapmuxRows'
import { dividerMetaFromMessage } from './model/divider'
import { normalizeOutputFilePaths } from './model/outputFilePaths'
import { resolveControlResponseSummary } from './persistedControlResponse'
import { pluginFor } from './providers/registry'

const logger = createLogger('rowExtraction')

/** The empty span context for a caller that resolved no sibling row. */
const NO_SPAN: ToolSpanContext = { request: undefined, result: undefined, role: 'other', visibleRows: { request: false, result: false } }

export interface RowExtractionOptions {
  /** The request and result of this row's tool span, already resolved. */
  span?: ToolSpanContext
  /** The stored span ID selects this row's call. */
  spanId?: string
  /** The worker's `span_type` column, which identifies the tool on every span row. */
  spanType?: string
  /** LeapMux's own reading of how the row ended, which a provider frame can contradict. */
  completion?: ProtoMessageCompletion
}

/**
 * Layer 1 returns a row, an unsupported frame, or an extraction failure.
 * Each outcome carries its completion state.
 * A hidden row remains a row. Callers must distinguish it from unsupported content and extraction failures.
 */
export type ChatRowExtraction = {
  /**
   * Use LeapMux's recorded completion first, then the assembled envelope's completion, then null.
   * Every outcome shares this value. The transcript and scroll rail must show the same interruption state.
   */
  completion: MessageCompletion | null
} & (
  | { kind: 'row', row: ChatRow }
  /**
   * No extractor produced a row. Keep the original payload available to the caller.
   * This can indicate an absent plugin or extractor. A registered extractor can reject an unknown frame also.
   */
  | { kind: 'unsupported', payload: unknown }
  /** Extraction threw. Keep the error and payload, and log the failure once. */
  | { kind: 'failed', payload: unknown, error: unknown }
)

/**
 * The row a reader draws from, or null when layer 1 produced none.
 *
 * The scroll rail and image tab use null when no row exists.
 * The transcript uses the other outcomes to show unsupported payloads and extraction failures.
 */
export function extractedRow(extraction: ChatRowExtraction): ChatRow | null {
  return extraction.kind === 'row' ? extraction.row : null
}

/**
 * Read one message into the shared row model, through its own provider's plugin.
 *
 * This function supplies the only entry into layer 1.
 * The transcript and its toolbar use this result. The scroll rail and image tab use it also.
 * Call `prepareChatRow` in ~/components/chat/rowPreparation.ts to resolve and classify the payload first.
 * This function requires that resolved payload and its classification.
 */
export function extractChatRow(
  agentProvider: AgentProvider | undefined,
  parsed: ResolvedMessageContent,
  category: MessageCategory,
  options: RowExtractionOptions = {},
): ChatRowExtraction {
  const plugin = pluginFor(agentProvider)
  const payload = parsed.parentObject ?? parsed.topLevel
  // The stored completion column takes precedence over the provider payload.
  // Isolated callers can pass that column through parsed.completion.
  const recorded = messageCompletionFromProto(options.completion ?? parsed.completion)
  // Frame parsing can throw. A saved control answer must remain readable even if its original frame fails to parse.
  let completion = recorded
  const row = (value: ChatRow): ChatRowExtraction => ({ kind: 'row', row: value, completion })
  const unsupported = (): ChatRowExtraction => ({ kind: 'unsupported', payload, completion })

  try {
    // The Worker stores control responses in its own metadata.
    // Resolve them before parsing the provider frame, which can be unreadable.
    // The shared row model keeps this answer available to every reader.
    // Run the provider's display hook here once.
    if (category.kind === 'control_response')
      return row({ kind: 'control-response', display: resolveControlResponseSummary(category.response, plugin?.controls?.controlResponseDisplay) })

    // The Worker joins streamed chunks and stores completion in the assembled envelope.
    // A set completion column takes precedence over that envelope.
    const assembled = parseAssembledMessage(parsed.parentObject)
    completion = recorded ?? assembled?.completion ?? null

    // Hidden rows draw nothing for every provider. Return their shared row kind directly.
    // Callers must distinguish a hidden row from a frame that no extractor can read.
    // An unsupported provider reaches the absent-plugin path below.
    // MessageBubble identifies that configuration problem from the tab metadata.
    if (category.kind === 'hidden')
      return row({ kind: 'hidden' })
    // The Worker owns the assembled envelope, so handle it before provider extraction.
    // Classification selects the row kind. The envelope supplies its text.
    // The classifier gives the stored assembled_kind column precedence over the envelope's kind field.
    if (assembled) {
      switch (category.kind) {
        case 'assistant_thinking':
          return row({ kind: 'assistant-thinking', text: assembled.text })
        case 'assistant_plan':
          return row({ kind: 'assistant-plan', text: assembled.text })
        case 'assistant_text':
          return row({ kind: 'assistant-text', text: assembled.text })
      }
    }
    // The plugin reads its native turn-end frame for the divider label.
    // The shared metadata reader adds the Worker's totals.
    // Pass the native payload to the plugin. Pass the parsed wrapper to the metadata reader, which unwraps it itself.
    if (category.kind === 'result_divider') {
      const divider = plugin?.transcript.extractDivider?.(payload, options.completion)
      const meta = dividerMetaFromMessage(parsed)
      return divider
        ? row({ kind: 'divider', divider: { ...divider, ...(meta === undefined ? {} : { meta }) } })
        : unsupported()
    }
    // Classification supplies parsed notification entries. Every reader uses those same model values.
    if (category.kind === 'notification') {
      return category.entries.length > 0
        ? row({ kind: 'notification', thread: { entries: category.entries } })
        : unsupported()
    }
    // LeapMux stores user messages as {content, attachments?} without a provider envelope.
    // Read them here even if the tab's provider metadata is absent during hydration.
    // The transcript and scroll rail must both receive that same user row.
    if (category.kind === 'user_content') {
      const userRow = leapmuxUserRow(parsed.parentObject)
      return userRow ? row(userRow) : unsupported()
    }
    const extract = plugin?.transcript.extractRow
    if (!extract)
      return unsupported()
    const metadata = isObject(parsed.messageMetadata) ? parsed.messageMetadata : undefined
    const snapshotValue = metadata?.[MESSAGE_METADATA_FIELD.TodoSnapshot]
    const todoSnapshot = snapshotValue === undefined ? null : protoJsonTodoToItem(snapshotValue)
    const input: RowExtractionInput = {
      resolved: parsed,
      category,
      span: options.span ?? NO_SPAN,
      ...(options.spanId === undefined ? {} : { spanId: options.spanId }),
      ...(options.spanType === undefined ? {} : { spanType: options.spanType }),
      ...(options.completion === undefined ? {} : { completion: options.completion }),
      ...(todoSnapshot !== null ? { todoSnapshot } : {}),
      ...(snapshotValue === undefined
        ? { todoSnapshotDiagnostic: 'TaskUpdate metadata is missing todo_snapshot; the persisted row is corrupted.' }
        : todoSnapshot === null
          ? { todoSnapshotDiagnostic: 'TaskUpdate metadata contains an invalid todo_snapshot; the persisted row is corrupted.' }
          : {}),
    }
    const extracted = extract(input)
    if (!extracted)
      return unsupported()
    const outputFilePaths = plugin?.transcript.outputFilePaths
    if (extracted.kind !== 'tool' || !outputFilePaths)
      return row(extracted)
    const paths = normalizeOutputFilePaths(outputFilePaths(input, extracted.call))
    return paths.length > 0
      ? row({ ...extracted, call: { ...extracted.call, outputFilePaths: paths } })
      : row(extracted)
  }
  catch (err) {
    // Return an extraction failure instead of throwing into an effect or promise outside the render tree.
    // Log it once here. Keep the error distinct from an unsupported provider frame.
    logger.warn('Failed to read a row', err)
    return { kind: 'failed', payload, error: err, completion }
  }
}
