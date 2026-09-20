import type { MessageCategory } from './messageClassifier'
import type { RenderContext } from './messageRenderers'
import type { ChatRowExtraction } from './rowExtraction'
import type { ResolvedMessageContent, ToolSpanContext } from './rowExtractionTypes'
import type { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { fixedCacheKey } from './messageRenderCache'
import { extractChatRow } from './rowExtraction'

export type RowExtractionContext = Pick<RenderContext, 'renderCache' | 'sources' | 'spanType'>

function rowSpanContext(context: RowExtractionContext | undefined): ToolSpanContext {
  return {
    request: context?.sources?.request(),
    result: context?.sources?.result(),
    role: context?.sources?.role() ?? 'other',
    visibleRows: context?.sources?.visibleRows() ?? { request: false, result: false },
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
    && left.span.result === right.span.result
    && left.span.role === right.span.role
    && left.span.visibleRows.request === right.span.visibleRows.request
    && left.span.visibleRows.result === right.span.visibleRows.result
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
    ...(completion !== undefined ? { completion } : {}),
    ...(context?.spanType !== undefined ? { spanType: context.spanType } : {}),
  })
  cache?.set(ROW_CACHE_KEY, { extraction, dependencies })
  return extraction
}
