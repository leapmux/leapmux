import type { JSX } from 'solid-js'
import type { MessageCategory } from '~/components/chat/messageClassifier'
import type { MessageRenderSources } from '~/components/chat/messageContextResolver'
import type { RowRenderContext } from '~/components/chat/renderContext'
import type { ChatRowExtraction } from '~/components/chat/rowExtraction'
import type { RowExtractionContext, RowExtractionSources } from '~/components/chat/rowModelCache'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { completionMarker, messageCompletionFromProto } from '~/components/chat/assembledMessage'
import { UnrecognizedMessage } from '~/components/chat/messageRenderers'
import { resolveMessageForRendering } from '~/components/chat/providers/registry'
import { pendingRowRequest, ToolPendingMessage } from '~/components/chat/results/ToolPendingMessage'
import { extractedRow } from '~/components/chat/rowExtraction'
import { cachedChatRow } from '~/components/chat/rowModelCache'
import { renderExtractedRow } from '~/components/chat/rowRenderers'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { createLogger } from '~/lib/logger'

const logger = createLogger('messageContentRenderer')

/** Focused capabilities for isolated extraction-and-render callers such as tests. */
export interface MessageContentRenderContext extends RowRenderContext, Omit<RowExtractionContext, 'sources'> {
  sources?: RowExtractionSources & Pick<MessageRenderSources, 'current'>
}

/** A minimal parsed message for an isolated caller that supplies no resolved source. */
function parsedMessageOf(parsed: unknown): ParsedMessageContent {
  const parentObject = isObject(parsed)
    ? parsed
    : undefined
  return {
    wrapper: null,
    topLevel: parentObject ?? null,
    parentObject,
    rawText: '',
    supplementalContent: undefined,
    messageMetadata: undefined,
  }
}

/**
 * Extract an isolated payload and render the PENDING row of its tool call: the preview of
 * what the call asked and its live output, as the list draws below the request row while
 * the call runs (see ToolPendingMessage). Renders nothing for a row that is not the
 * request row of a call.
 */
export function renderPendingMessageContent(
  parsed: unknown,
  context?: MessageContentRenderContext,
  category?: MessageCategory,
  agentProvider?: AgentProvider,
  messageCompletion?: MessageCompletion,
): JSX.Element {
  const resolved = context?.sources?.current()
    ?? resolveMessageForRendering(parsedMessageOf(parsed), agentProvider ?? AgentProvider.UNSPECIFIED)
  const row = pendingRowRequest(extractedRow(cachedChatRow(context, agentProvider, resolved, category ?? { kind: 'unknown' }, messageCompletion)))
  if (row === null)
    return null
  return (
    <ToolPendingMessage
      row={row}
      {...(context !== undefined
        ? { context }
        : {})}
    />
  )
}

/**
 * Extract and render an isolated payload as the list draws it: its row, and below the row
 * of a call that runs, the pending row that states what the call asks until the result row
 * takes its place (see renderPendingMessageContent). Only a request row draws one.
 */
export function renderListedMessageContent(
  parsed: unknown,
  context?: MessageContentRenderContext,
  category?: MessageCategory,
  agentProvider?: AgentProvider,
  messageCompletion?: MessageCompletion,
): JSX.Element {
  return (
    <>
      {renderMessageContent(parsed, context, category, agentProvider, messageCompletion)}
      {renderPendingMessageContent(parsed, context, category, agentProvider, messageCompletion)}
    </>
  )
}

/** Extract and render an isolated payload, or render the caller's prepared extraction. */
export function renderMessageContent(
  parsedOrRawJson: unknown,
  context?: MessageContentRenderContext,
  category?: MessageCategory,
  agentProvider?: AgentProvider,
  messageCompletion?: MessageCompletion,
  extracted?: ChatRowExtraction,
): JSX.Element {
  try {
    const current = context?.sources?.current()
    if (extracted)
      return renderExtractedRow(extracted, context, current?.messageMetadata)

    const resolved = current
      ?? resolveMessageForRendering(
        category?.kind === 'control_response'
          ? parsedMessageOf(undefined)
          : parsedMessageOf(typeof parsedOrRawJson === 'string'
              ? JSON.parse(parsedOrRawJson)
              : parsedOrRawJson),
        agentProvider ?? AgentProvider.UNSPECIFIED,
      )

    return renderExtractedRow(
      cachedChatRow(context, agentProvider, resolved, category ?? { kind: 'unknown' }, messageCompletion),
      context,
      resolved.messageMetadata,
    )
  }
  catch (error) {
    logger.warn('Failed to render message content:', error)
  }

  const fallback = (
    <UnrecognizedMessage
      payload={parsedOrRawJson}
      renderFailed
      {...(context !== undefined
        ? { context }
        : {})}
    />
  )
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
