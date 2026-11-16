import type { MessageCategory } from './messageClassifier'
import type { MessageRenderSources, ResolvedMessage } from './messageContextResolver'
import type { ToolCall } from './model/toolCall'
import type { MarkdownRenderContext } from './renderContext'
import type { ChatRowExtraction } from './rowExtraction'
import type { ResolvedMessageContent, ToolSpanContext } from './rowExtractionTypes'
import type { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { fixedCacheKey } from './messageRenderCache'
import { extractChatRow, extractedRow } from './rowExtraction'
import { prepareChatRow } from './rowPreparation'

/**
 * The sources one row extraction reads. No member gives the row a LATER message of its
 * span: a request row never reads its result (see ToolSpanContext).
 */
export type RowExtractionSources = Pick<MessageRenderSources, 'request' | 'role' | 'visibleRows'>

export interface RowExtractionContext extends Pick<MarkdownRenderContext, 'renderCache'> {
  sources?: RowExtractionSources
  spanType?: string | undefined
}

function rowSpanContext(context: RowExtractionContext | undefined): ToolSpanContext {
  return {
    request: context?.sources?.request(),
    role: context?.sources?.role() ?? 'other',
    visibleRows: context?.sources?.visibleRows() ?? { request: false },
  }
}

interface CachedRowEntry {
  extraction: ChatRowExtraction
  dependencies: RowModelDependencies
}

interface RowModelDependencies {
  resolved: ResolvedMessageContent
  categoryKind: MessageCategory['kind']
  completion: MessageCompletion | undefined
  spanType: string | undefined
  span: ToolSpanContext
}

function sameDependencies(left: RowModelDependencies, right: RowModelDependencies): boolean {
  return left.resolved === right.resolved
    && left.categoryKind === right.categoryKind
    && left.completion === right.completion
    && left.spanType === right.spanType
    && left.span.request === right.span.request
    && left.span.role === right.span.role
    && left.span.visibleRows.request === right.span.visibleRows.request
}

const ROW_CACHE_KEY = fixedCacheKey<CachedRowEntry>('model.row')

export function cachedChatRow(
  context: RowExtractionContext | undefined,
  agentProvider: AgentProvider | undefined,
  resolved: ResolvedMessageContent,
  category: MessageCategory,
  completion: MessageCompletion | undefined,
): ChatRowExtraction {
  const cache = context?.renderCache
  const span = rowSpanContext(context)
  const dependencies: RowModelDependencies = {
    resolved,
    categoryKind: category.kind,
    completion,
    spanType: context?.spanType,
    span,
  }
  const cached = cache?.get(ROW_CACHE_KEY)
  if (cached && sameDependencies(cached.dependencies, dependencies))
    return cached.extraction
  const extraction = extractChatRow(agentProvider, resolved, category, {
    span,
    ...(completion !== undefined
      ? { completion }
      : {}),
    ...(context?.spanType !== undefined
      ? { spanType: context.spanType }
      : {}),
  })
  cache?.set(ROW_CACHE_KEY, { extraction, dependencies })
  return extraction
}

/** The header calls extracted so far, by the result and then by the request of a span. */
const headerCalls = new WeakMap<ResolvedMessage, WeakMap<ResolvedMessageContent, { call: ToolCall | undefined }>>()

/**
 * The call that heads the request row of a span (see ToolMessage's `header`): the call
 * that the result row draws, read through the same extraction, with `request` as the
 * span's request.
 *
 * Every copy of every row that draws no result asks for it: the listed row, its hidden
 * copy, and an update row. The resolver hands the same object for one revision of a side,
 * so this extracts the result once for each pair of revisions, not once for each copy,
 * and a row that scrolls back in finds the call at once. The result row's classification
 * takes no child-transcript context: that context decides only how a user message reads,
 * never how a tool result does.
 */
export function cachedSpanHeaderCall(result: ResolvedMessage, request: ResolvedMessageContent): ToolCall | undefined {
  let byRequest = headerCalls.get(result)
  if (byRequest === undefined) {
    byRequest = new WeakMap()
    headerCalls.set(result, byRequest)
  }
  const cached = byRequest.get(request)
  if (cached !== undefined)
    return cached.call
  const header = extractedRow(prepareChatRow(result.message, {
    original: result.original,
    resolved: result.resolved,
    span: {
      request,
      role: 'result',
      visibleRows: { request: true },
    },
  }).extraction)
  const call = header?.kind === 'tool' && header.role === 'result'
    ? header.call
    : undefined
  byRequest.set(request, { call })
  return call
}
