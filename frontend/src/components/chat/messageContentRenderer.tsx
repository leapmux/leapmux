import type { JSX } from 'solid-js'
import type { MessageCategory } from './messageClassifier'
import type { RenderContext } from './messageRenderers'
import type { ChatRowExtraction } from './rowExtraction'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { createLogger } from '~/lib/logger'
import { completionMarker, messageCompletionFromProto } from './assembledMessage'
import { UnrecognizedMessage } from './messageRenderers'
import { resolveMessageForRendering } from './providers/registry'
import { cachedChatRow } from './rowModelCache'
import { renderExtractedRow } from './rowRenderers'

const logger = createLogger('messageContentRenderer')

/** A minimal parsed message for an isolated caller that supplies no resolved source. */
function parsedMessageOf(parsed: unknown): ParsedMessageContent {
  const parentObject = isObject(parsed) ? parsed : undefined
  return { wrapper: null, topLevel: parentObject ?? null, parentObject, rawText: '', supplementalContent: undefined, messageMetadata: undefined }
}

/** Extract and render an isolated payload, or render the caller's prepared extraction. */
export function renderMessageContent(
  parsedOrRawJson: unknown,
  context?: RenderContext,
  category?: MessageCategory,
  agentProvider?: AgentProvider,
  messageCompletion?: MessageCompletion,
  extracted?: ChatRowExtraction,
): JSX.Element {
  try {
    if (extracted)
      return renderExtractedRow(extracted, context)

    const resolved = context?.sources?.current()
      ?? resolveMessageForRendering(
        category?.kind === 'control_response'
          ? parsedMessageOf(undefined)
          : parsedMessageOf(typeof parsedOrRawJson === 'string' ? JSON.parse(parsedOrRawJson) : parsedOrRawJson),
        agentProvider ?? AgentProvider.UNSPECIFIED,
      )

    return renderExtractedRow(
      cachedChatRow(context, agentProvider, resolved, category ?? { kind: 'unknown' }, messageCompletion),
      context,
    )
  }
  catch (error) {
    logger.warn('Failed to render message content:', error)
  }

  const fallback = <UnrecognizedMessage payload={parsedOrRawJson} renderFailed {...(context !== undefined ? { context } : {})} />
  const marker = completionMarker(messageCompletionFromProto(messageCompletion))
  return marker
    ? (
        <>
          {fallback}
          <div role="note">{marker}</div>
        </>
      )
    : fallback
}
