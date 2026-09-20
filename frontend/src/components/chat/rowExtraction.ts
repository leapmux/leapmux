import type { MessageCompletion } from './assembledMessage'
import type { MessageCategory } from './messageClassifier'
import type { ChatRow } from './model/row'
import type { ResolvedMessageContent } from './rowExtractionTypes'
import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import type { AgentProvider, MessageCompletion as ProtoMessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { isObject } from '~/lib/jsonPick'
import { createLogger } from '~/lib/logger'
import { protoJsonTodoToItem } from '~/stores/chatTodoStore'
import { messageCompletionFromProto, parseAssembledMessage } from './assembledMessage'
import { leapmuxUserRow } from './leapmuxRows'
import { dividerMetaFromMessage } from './model/divider'
import { notificationEntriesFor } from './notificationEntries'
import { resolveControlResponseSummary } from './persistedControlResponse'
import { pluginFor } from './providers/registry'

const logger = createLogger('rowExtraction')

/** The three sides a caller resolved no span for. */
const NO_SPAN: ToolSpanContext = { request: undefined, result: undefined, role: 'other', visibleRows: { request: false, result: false } }

export interface RowExtractionOptions {
  /** The three sides of this row's tool span, already resolved. */
  span?: ToolSpanContext
  /** The worker's `span_type` column, which identifies the tool on every span row. */
  spanType?: string
  /** LeapMux's own reading of how the row ended, which a provider frame can contradict. */
  completion?: ProtoMessageCompletion
}

/**
 * What layer 1 answered for one message, and WHY when it answered no row.
 *
 * The three outcomes used to be one `null`, and the two readers that had to tell them
 * apart each rebuilt the distinction from something else. A row nobody could read and
 * a row a plugin THREW on drew the same card, so a defect in LeapMux read as a
 * provider sending a frame LeapMux has no display for. And a `hidden` category
 * answered null as well, so the scroll rail and the image tab could not tell a row
 * that draws nothing from a row that failed.
 */
export type ChatRowExtraction = {
  /**
   * How the message ended: LeapMux's own reading, else the assembled envelope's own
   * statement, else null.
   *
   * Stated ONCE here because every outcome below draws the same completion chrome,
   * and the two readers that derived it themselves disagreed -- the transcript took
   * the envelope's word when the worker's column was unset and the scroll rail did
   * not, so one interrupted thought carried its notice and the dot beside it did not.
   */
  completion: MessageCompletion | null
} & (
  | { kind: 'row', row: ChatRow }
  /**
   * No reader could produce a row: the provider has no plugin, the plugin has no
   * extractor, or the extractor read the frame and found nothing it knows. The frame
   * itself is the only content the row has, so it travels with the outcome.
   */
  | { kind: 'unsupported', payload: unknown }
  /** The extraction THREW. A defect in LeapMux, logged once at the extraction. */
  | { kind: 'failed', payload: unknown, error: unknown }
)

/**
 * The row a reader draws from, or null when layer 1 produced none.
 *
 * For the readers that have nothing to show for an unreadable frame -- the scroll-rail
 * preview and the image tab's index lookup -- rather than for the transcript, which
 * draws the frame itself through {@link ChatRowExtraction}'s other two outcomes.
 */
export function extractedRow(extraction: ChatRowExtraction): ChatRow | null {
  return extraction.kind === 'row' ? extraction.row : null
}

/**
 * Read one message into the shared row model, through its own provider's plugin.
 *
 * The ONE entry into layer 1, for every reader of a row: the transcript renderer
 * draws from it, the bubble's toolbar derives its actions from it, the scroll
 * rail previews from it, and an image tab addresses a picture by its index in
 * it. They used to ask four separate provider hooks the same question, and a
 * fifth reader always found the one that had drifted.
 *
 * Reach it through `prepareChatRow` (~/components/chat/rowPreparation.ts), which
 * resolves the supplemental content and classifies the resolved payload first. This
 * function takes both as given: a caller that classified the RAW payload and
 * extracted the resolved one gets a row its own category contradicts.
 */
export function extractChatRow(
  agentProvider: AgentProvider | undefined,
  parsed: ResolvedMessageContent,
  category: MessageCategory,
  options: RowExtractionOptions = {},
): ChatRowExtraction {
  const plugin = pluginFor(agentProvider)
  const payload = parsed.parentObject ?? parsed.topLevel
  // LeapMux's OWN reading of how the row ended. `parsed.completion` is the same column
  // read off the payload the caller resolved, for the one caller that has no message
  // to read it from -- an isolated render that passes a payload and a context and
  // nothing else.
  const recorded = messageCompletionFromProto(options.completion ?? parsed.completion)
  // Filled inside the guard below, because reading the frame can throw: the row that
  // proves it is a saved control answer whose ORIGINAL frame never parsed, which must
  // still state its answer.
  let completion = recorded
  const row = (value: ChatRow): ChatRowExtraction => ({ kind: 'row', row: value, completion })
  const unsupported = (): ChatRowExtraction => ({ kind: 'unsupported', payload, completion })

  try {
    // A control response is LeapMux's OWN row: the worker writes it, and the
    // classifier reads it out of the worker's metadata before any plugin is asked. It
    // reaches the model here rather than skipping it, so the transcript draws every row
    // through one switch and no reader has to know which categories take a path of
    // their own.
    //
    // It answers FIRST, ahead of every read of the frame below, and that order is the
    // point: the answer lives in the CATEGORY, and the original frame beside it can be
    // one that never parsed. A read placed ahead of this drew the unrecognized card
    // for such a row instead of the answer, which is the one thing it exists to state.
    //
    // The provider's derivation runs HERE, and its native payloads stop here with it.
    // Both surfaces that draw the answer ran the hook themselves, which is two
    // dispatches for one row and two places for the fallback to be forgotten.
    if (category.kind === 'control_response')
      return row({ kind: 'control-response', display: resolveControlResponseSummary(category.response, plugin?.controls?.controlResponseDisplay) })

    // The worker joins a run of streamed chunks into ONE assembled message and states
    // how that run ended inside the envelope. LeapMux's own column wins where it is
    // set, because a frame can contradict what LeapMux concluded about the turn.
    const assembled = parseAssembledMessage(parsed.parentObject)
    completion = recorded ?? assembled?.completion ?? null

    // A HIDDEN row draws nothing, whatever its provider. The rule lives HERE so every
    // reader of layer 1 gets it -- `renderMessageContent` applied it before calling
    // this, but the scroll rail and the image tab did not, and they relied on all six
    // `extractRow` switches happening to fall through for the category.
    //
    // A `hidden` ROW rather than no row: "this row draws nothing" and "nobody could
    // read this frame" are different answers, and folding them together sent every
    // hidden row to the unrecognized card the moment a reader stopped special-casing
    // the category.
    //
    // `unsupported_provider` takes NO branch of its own, and that is deliberate. It
    // has no plugin by definition, so it reaches `unsupported` below on its own --
    // which is the true answer: nobody can read the frame. `MessageBubble` states the
    // misconfiguration itself, because only the transcript knows to blame the tab's
    // metadata rather than the provider.
    if (category.kind === 'hidden')
      return row({ kind: 'hidden' })
    // The assembled envelope answers before any plugin does: the worker writes that
    // shape and no provider ever sends it, so no plugin can recognize it. The three
    // readers each used to parse it themselves, and the completion marker landed in
    // the text on one path and in a note beside it on another.
    //
    // The CATEGORY picks the row kind and the envelope supplies only the words. The
    // classifier reads the worker's `assembled_kind` column first and the envelope's
    // own `kind` field second; a reader that took the field alone answered a
    // different row than the list had measured whenever the two disagreed.
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
    // A turn end is a cross-provider surface, like the question and the elicitation:
    // every provider ends a turn and each states it in its own frame, so the plugin
    // reads ITS frame for the label and the shared half adds the totals the worker
    // measured -- which are the same fields on every provider, injected by the worker.
    //
    // The plugin is handed the provider's OWN object, not this wrapper: each reader
    // matches its native envelope (`parsed.type === 'result'`), which the wrapper does
    // not carry. The shared meta reader takes the wrapper, because it unwraps to the
    // inner message itself.
    if (category.kind === 'result_divider') {
      const divider = plugin?.transcript.extractDivider?.(payload, options.completion)
      const meta = dividerMetaFromMessage(parsed)
      return divider
        ? row({ kind: 'divider', divider: { ...divider, ...(meta === undefined ? {} : { meta }) } })
        : unsupported()
    }
    // A notification thread is cross-provider for the same reason: the worker threads
    // consecutive notifications into one row, and `notificationEntriesFor` reads each
    // message -- worker-written ones through the shared table, the rest through the
    // plugin. The category already carries the thread's messages, so nothing re-reads
    // the wrapper here.
    //
    // A thread that states NOTHING yields no row. It falls to the unrecognized card,
    // which is what the legacy path did by falling back to the raw-JSON renderer.
    if (category.kind === 'notification') {
      const entries = category.messages.flatMap(message =>
        isObject(message) ? notificationEntriesFor(message, agentProvider) : [])
      return entries.length > 0 ? row({ kind: 'notification', thread: { entries } }) : unsupported()
    }
    // A user row is LeapMux's OWN row too: LeapMux persists it as a flat
    // `{content, attachments?}` object that carries no provider frame, and every
    // plugin answered it with this same helper. Answering it HERE is what keeps the
    // row readable for a provider that has no plugin at all -- a tab can lack worker
    // metadata while hydration runs, so its provider can be UNSPECIFIED, and
    // `classifyMessage` still gives such a message `user_content`. `messageContentRenderer`
    // draws that row outside the `extractRow` branch, so the transcript showed the
    // text while the scroll rail's dot lost its preview.
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
    const extracted = extract({
      resolved: parsed,
      category,
      span: options.span ?? NO_SPAN,
      ...(options.spanType === undefined ? {} : { spanType: options.spanType }),
      ...(options.completion === undefined ? {} : { completion: options.completion }),
      ...(todoSnapshot !== null ? { todoSnapshot } : {}),
      ...(snapshotValue === undefined
        ? { todoSnapshotDiagnostic: 'TaskUpdate metadata is missing todo_snapshot; the persisted row is corrupted.' }
        : todoSnapshot === null
          ? { todoSnapshotDiagnostic: 'TaskUpdate metadata contains an invalid todo_snapshot; the persisted row is corrupted.' }
          : {}),
    })
    return extracted ? row(extracted) : unsupported()
  }
  catch (err) {
    // A malformed frame must degrade to "no row", never propagate: three of the
    // four readers run outside the render tree, where a throw reaches an effect
    // or a promise chain that has no way to draw the failure.
    //
    // Logged ONCE, here, and marked `failed` so the card the reader gets says LeapMux
    // could not draw the row rather than that LeapMux has no display for it. Each
    // reader used to log its own line for the same throw, and the transcript's card
    // blamed the provider.
    logger.warn('Failed to read a row', err)
    return { kind: 'failed', payload, error: err, completion }
  }
}
