import type { MessageCategory } from './messageClassifier'
import type { MessageRenderSources, ResolvedMessage } from './messageContextResolver'
import type { ToolCall } from './model/toolCall'
import type { MarkdownRenderContext } from './renderContext'
import type { ChatRowExtraction } from './rowExtraction'
import type { ResolvedMessageContent, ToolSpanContext } from './rowExtractionTypes'
import type { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { SettingsLabelRead } from '~/lib/settingsLabelCache'
import { refreshSettingsLabelRead } from '~/lib/settingsLabelCache'
import { fixedCacheKey } from './messageRenderCache'
import { extractChatRow, extractedRow } from './rowExtraction'
import { prepareMessage } from './rowPreparation'

/**
 * The sources one row extraction reads. No member gives the row a LATER message of its
 * span: a request row never reads its result (see ToolSpanContext).
 */
export type RowExtractionSources = Pick<MessageRenderSources, 'request' | 'role' | 'visibleRows'>

export interface RowExtractionContext extends Pick<MarkdownRenderContext, 'renderCache'> {
  sources?: RowExtractionSources
  spanType?: string | undefined
  spanId?: string | undefined
}

function rowSpanContext(context: RowExtractionContext | undefined): ToolSpanContext {
  return {
    request: context?.sources?.request(),
    role: context?.sources?.role() ?? 'other',
    visibleRows: context?.sources?.visibleRows() ?? { request: false },
  }
}

/**
 * One row's extraction, with the label groups that it read.
 *
 * An extraction can word a value by its settings label, such as the permission mode of a
 * plan approval or the model of a Codex spawn. The labels often arrive after the row
 * draws, and nothing else about the row changes then, so the extraction is reused only
 * while those labels are unchanged. The entry cache cannot carry these groups: it
 * classifies a row, and only the extraction reads them.
 */
interface CachedRowEntry {
  extraction: SettingsLabelRead<ChatRowExtraction>
  dependencies: RowModelDependencies
}

interface RowModelDependencies {
  resolved: ResolvedMessageContent
  categoryKind: MessageCategory['kind']
  completion: MessageCompletion | undefined
  provider: AgentProvider | undefined
  spanId: string | undefined
  spanType: string | undefined
  span: ToolSpanContext
}

function sameDependencies(left: RowModelDependencies, right: RowModelDependencies): boolean {
  return left.resolved === right.resolved
    && left.categoryKind === right.categoryKind
    && left.completion === right.completion
    && left.provider === right.provider
    && left.spanId === right.spanId
    && left.spanType === right.spanType
    && left.span.request === right.span.request
    && left.span.role === right.span.role
    && left.span.visibleRows.request === right.span.visibleRows.request
}

const ROW_CACHE_KEY = fixedCacheKey<CachedRowEntry>('model.row')
const noRequest = {}
const resultExtractions = new WeakMap<ResolvedMessageContent, WeakMap<object, Map<string, SettingsLabelRead<ChatRowExtraction>>>>()

/** A request header and a result body can consume exactly the same extracted tool. */
function resultCache(deps: RowModelDependencies): Map<string, SettingsLabelRead<ChatRowExtraction>> {
  let byRequest = resultExtractions.get(deps.resolved)
  if (byRequest === undefined) {
    byRequest = new WeakMap()
    resultExtractions.set(deps.resolved, byRequest)
  }
  const request = deps.span.request ?? noRequest
  let entries = byRequest.get(request)
  if (entries === undefined) {
    entries = new Map()
    byRequest.set(request, entries)
  }
  return entries
}

/**
 * The row model of one message, read through the shared extraction and kept in the row's
 * render cache.
 *
 * The cached row serves while its inputs and the labels that it read are unchanged (see
 * CachedRowEntry). The check reads the label catalog, so a computation that draws the row
 * runs again when the labels change, and draws a new row only when the row read one of
 * them.
 */
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
    provider: agentProvider,
    spanId: context?.spanId,
    spanType: context?.spanType,
    span,
  }
  const cached = cache?.get(ROW_CACHE_KEY)
  const own = cached !== undefined && sameDependencies(cached.dependencies, dependencies)
    ? cached.extraction
    : undefined
  const extract = (): ChatRowExtraction => extractChatRow(agentProvider, resolved, category, {
    span,
    ...(completion !== undefined
      ? { completion }
      : {}),
    ...(context?.spanType !== undefined
      ? { spanType: context.spanType }
      : {}),
    ...(context?.spanId !== undefined
      ? { spanId: context.spanId }
      : {}),
  })
  const extraction = refreshSettingsLabelRead(own, () => {
    if (span.role !== 'result' || (category.kind !== 'tool_use' && category.kind !== 'tool_result'))
      return extract()
    const shared = resultCache(dependencies)
    const sharedKey = JSON.stringify([
      agentProvider,
      category.kind,
      completion,
      dependencies.spanId,
      dependencies.spanType,
      span.visibleRows.request,
    ])
    // The shared extraction reports its label groups to this read, also when it is reused.
    const sharedExtraction = refreshSettingsLabelRead(shared.get(sharedKey), extract)
    shared.set(sharedKey, sharedExtraction)
    return sharedExtraction.value
  })
  if (extraction !== own)
    cache?.set(ROW_CACHE_KEY, { extraction, dependencies })
  return extraction.value
}

/**
 * The header calls extracted so far, by the result and then by the request of a span,
 * each with the label groups that its extraction read (see CachedRowEntry).
 */
const headerCalls = new WeakMap<ResolvedMessage, WeakMap<ResolvedMessageContent, SettingsLabelRead<ToolCall | undefined>>>()

/**
 * The call that heads the request row of a span (see ToolMessage's `header`): the call
 * that the result row draws, read through the same extraction, with `request` as the
 * span's request.
 *
 * Every copy of every row that draws no result asks for it: the listed row, its hidden
 * copy, and an update row. The resolver hands the same object for one revision of a side,
 * so this extracts the result once for each pair of revisions and each revision of the
 * labels that the call read, not once for each copy, and a row that scrolls back in finds
 * the call at once. The result row's classification takes no child-transcript context:
 * that context decides only how a user message reads, never how a tool result does.
 */
export function cachedSpanHeaderCall(result: ResolvedMessage, request: ResolvedMessageContent): ToolCall | undefined {
  let byRequest = headerCalls.get(result)
  if (byRequest === undefined) {
    byRequest = new WeakMap()
    headerCalls.set(result, byRequest)
  }
  const cached = byRequest.get(request)
  const header = refreshSettingsLabelRead(cached, () => {
    const prepared = prepareMessage(result.message, {
      original: result.original,
      resolved: result.resolved,
    })
    const row = extractedRow(cachedChatRow({
      spanId: result.message.spanId,
      spanType: result.message.spanType,
      sources: {
        request: () => request,
        role: () => 'result',
        visibleRows: () => ({ request: true }),
      },
    }, result.message.agentProvider, prepared.resolved, prepared.category, result.message.completion))
    return row?.kind === 'tool' && row.role === 'result'
      ? row.call
      : undefined
  })
  if (header !== cached)
    byRequest.set(request, header)
  return header.value
}
