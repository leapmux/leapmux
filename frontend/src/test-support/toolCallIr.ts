import type { McpContentItem } from '~/components/chat/ir/mcpToolCall'
import type { ChatRowIR, ToolCallRow, ToolRowRole, ToolSpanRows } from '~/components/chat/ir/row'
import type { ToolCallCommon, ToolCallIR, ToolCallOfKinds, ToolCallPayloadOf, ToolResultOf } from '~/components/chat/ir/toolCall'
import type { ToolKind } from '~/components/chat/ir/toolKind'
import type { ToolRowStatus } from '~/components/chat/ir/toolRowStatus'
import type { ToolRequests, ToolResults } from '~/components/chat/ir/tools'
import type { MessageCategory } from '~/components/chat/messageClassification'
import type { SpanRole } from '~/components/chat/providers/registry'
import type { ToolResultMeta } from '~/components/chat/results/tools/meta'
import type { RowExtractionOptions } from '~/components/chat/rowExtraction'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { imagesForIR, quotableTextForIR } from '~/components/chat/ir/derivations'
import { toolCallRow } from '~/components/chat/ir/row'
import { buildToolCall } from '~/components/chat/ir/toolCall'
import { providerFor } from '~/components/chat/providers/registry'
import { input } from '~/components/chat/providers/testUtils'
import { toolCallMeta } from '~/components/chat/results/tools/meta'
import { parsedCall } from '~/components/chat/results/tools/renderer'
import { todoRenderer } from '~/components/chat/results/tools/todo'
import { extractChatRow, extractedRow } from '~/components/chat/rowExtraction'

/**
 * The smallest request each kind accepts. Total over ToolKind: a new kind fails to
 * compile HERE first.
 *
 * The four file-change kinds state a FILE, because invariant I7 says a call of those
 * kinds always does: the row composes its header from the list at every state of the
 * call, so an empty one heads the row with the operation word and nothing else. An
 * empty list here made every default-request test of those kinds build a call
 * `buildToolCall` refuses.
 */
export const MINIMAL_REQUEST: { [K in ToolKind]: ToolRequests[K] } = {
  '': { args: {} },
  'agent': { description: '', prompt: '' },
  'agents': {},
  'chart': { spec: '{}' },
  'delete': { changes: [{ filePath: '/p/a.ts', oldStr: '', newStr: '' }] },
  'edit': { changes: [{ filePath: '/p/a.ts', oldStr: '', newStr: '' }] },
  'execute': { command: 'true' },
  'fetch': { url: 'https://example.com' },
  'glob': { pattern: '*', paths: [] },
  'grep': { pattern: 'x', paths: [] },
  'image': {},
  'list': { path: '.' },
  'mcp': { args: {}, server: 's', tool: 't' },
  'memory': {},
  'message': { text: '' },
  'move': { changes: [{ filePath: '/p/a.ts', oldStr: '', newStr: '' }] },
  'other': { args: {} },
  'question': { questions: [] },
  'read': { path: '/p/a.ts' },
  'report': {},
  'search': { pattern: 'x', paths: [] },
  'skill': {},
  'switch_mode': {},
  'task': { action: 'other' },
  'think': { text: '' },
  'todo': { items: [] },
  'trigger': { action: 'other' },
  'wait': {},
  'web_search': { query: 'q' },
  'write': { changes: [{ filePath: '/p/a.ts', oldStr: '', newStr: '' }] },
}

/** The smallest result each kind answers with. Total over ToolKind, for the same reason. */
export const MINIMAL_RESULT: { [K in ToolKind]: ToolResults[K] } = {
  '': { content: [] },
  'agent': { agents: [] },
  'agents': { text: '', format: 'plain' },
  'chart': { shape: 'bar', labels: [], series: [] },
  'delete': { changes: [] },
  'edit': { changes: [] },
  'execute': { commands: [], unresolvedTerminals: [] },
  'fetch': { result: '' },
  'glob': { filenames: [], content: '', numFiles: 0, numLines: 0, truncated: false, fallbackContent: '', empty: false },
  'grep': { filenames: [], content: '', numFiles: 0, numLines: 0, truncated: false, fallbackContent: '', empty: false },
  'image': {},
  'list': { entries: [] },
  'mcp': { content: [] },
  'memory': { text: '', format: 'plain' },
  'message': { text: '', format: 'plain' },
  'move': { changes: [] },
  'other': { content: [] },
  'question': { answers: [] },
  'read': { lines: null, fallbackContent: '' },
  'report': { text: '', format: 'plain' },
  'search': { filenames: [], content: '', numFiles: 0, numLines: 0, truncated: false, fallbackContent: '', empty: false },
  'skill': { text: '', format: 'plain' },
  'switch_mode': { text: '', format: 'plain' },
  'task': { outcome: 'completed', output: '' },
  'think': { text: '', format: 'plain' },
  'todo': { items: [] },
  'trigger': { text: '', format: 'plain' },
  'wait': { text: '', format: 'plain' },
  'web_search': { links: [], summary: '' },
  'write': { changes: [] },
}

/**
 * The header a to-do row draws, composed by the renderer from the call's request.
 *
 * A provider states no `title` for this kind: `todoRenderer` derives the count, and
 * a copy in each plugin was a second place for the wording to drift. A test that
 * wants the words a reader sees asks the renderer for them.
 *
 * This reader DELEGATES rather than re-deriving. It held its own copy of the rule
 * once, and the copy went stale the moment the renderer stopped heading a cleared
 * list "To-do list cleared" -- four provider tests then pinned a header no row drew.
 */
export function todoTitleOf(call: ToolCallIR): string {
  if (call.kind !== 'todo')
    throw new Error(`todoTitleOf takes a todo call, not ${call.kind || 'an unstated kind'}`)
  const title = todoRenderer.title(parsedCall(call), undefined)
  if (typeof title !== 'string')
    throw new TypeError('todoRenderer.title answered markup, which this reader cannot state')
  return title
}

/**
 * A complete call of one kind, so a test states only the fields it is about.
 *
 * Answers {@link ToolCallOfKinds}, the DISTRIBUTED form, exactly as `toolCall` does.
 * `ToolCallOf<'read' | 'grep'>` is one object whose request is both kinds' requests at
 * once, which no real call satisfies and which `ToolCallIR` does not accept -- so a
 * table-driven test that passed its kind through a union could not hand the result to
 * anything that takes a call.
 */
export function toolCallIr<K extends ToolKind>(kind: K, overrides: ToolCallIrOverrides<K> = {}): ToolCallOfKinds<K> {
  // The same strip `toolCall` applies, for the same reason. An override may
  // state an explicit `undefined` (the six fields the interface allows it on),
  // and a plain spread would copy that undefined straight over the default and
  // build the row shape that makes `imagesForIR` throw -- a state the IR
  // declares impossible, handed to a renderer by the helper every kind test
  // builds through. A test must not be able to build what production cannot.
  const { id, status, name, images, request, result, ...rest } = overrides
  const rowStatus = status ?? 'completed'
  const built = buildToolCall<K>(
    { id: id ?? 'call-1', name: name ?? (kind || 'tool'), status: rowStatus },
    {
      kind,
      ...rest,
      request: request ?? MINIMAL_REQUEST[kind],
      // A COMPLETED call answers -- invariant I2 -- so a test that states no result
      // gets the kind's smallest one rather than a call the builder refuses. The
      // helper's contract is a complete call of the kind, and the empty result is
      // what completes it. Every other status admits no result and takes none.
      result: result ?? (rowStatus === 'completed' ? MINIMAL_RESULT[kind] : undefined),
      images: images ?? [],
    } as ToolCallPayloadOf<K>,
  )
  // A test must not be able to build what production cannot, so the helper routes
  // through the one validating builder and refuses a draft that breaks an invariant.
  // A test that WANTS an invalid draft calls `buildToolCall` itself and reads the
  // fault, which is the whole of the negative coverage.
  if (!built.ok)
    throw new Error(`toolCallIr cannot build a ${JSON.stringify(kind)} call: ${built.fault}`)
  return built.call
}

/**
 * The fields a test states, as ONE flat object over the kind's own request and result.
 *
 * `Partial<Omit<ToolCallOf<K>, …>>` cannot serve: `ToolCallOf<K>` is the lifecycle
 * UNION now, and `Omit` over a union collapses each member's `images` into a single
 * property whose type is `readonly []` for the unfinished half -- so a test that
 * states a picture on a completed call failed to compile against a variant it was not
 * using. This states each field once, at the kind.
 *
 * The six fields the helper strips in `toolCallIr` may carry an EXPLICIT
 * undefined, which is the input shape its defaults-exist test states;
 * `exactOptionalPropertyTypes` keeps undefined out of the rest.
 */
export interface ToolCallIrOverrides<K extends ToolKind> {
  id?: ToolCallCommon['id'] | undefined
  name?: ToolCallCommon['name'] | undefined
  title?: ToolCallCommon['title']
  label?: ToolCallCommon['label']
  icon?: ToolCallCommon['icon']
  metadata?: ToolCallCommon['metadata']
  status?: ToolRowStatus | undefined
  request?: ToolRequests[K] | undefined
  result?: ToolResultOf<K> | undefined
  images?: readonly ImageResultSource[] | undefined
  extraContent?: readonly McpContentItem[]
  truncated?: boolean
}

/** One mounted row around a call. */
export function toolRow(call: ToolCallIR, role: ToolRowRole = 'result', span: Partial<ToolSpanRows> = {}): ToolCallRow {
  return toolCallRow(call, role, { request: span.request ?? false, result: span.result ?? false })
}

/** The ONE tool call a provider frame becomes, or null when the frame draws no tool row. */
export function providerToolCall(provider: AgentProvider, payload: Record<string, unknown>, options: ProviderRowOptions = {}): ToolCallIR | null {
  const row = providerRowIr(provider, payload, options)
  return row?.kind === 'tool' ? row.call : null
}

/** What the bubble's toolbar offers for one frame, through the one call `MessageBubble` reads. */
export function providerToolMeta(provider: AgentProvider, payload: Record<string, unknown>, options: ProviderRowOptions = {}): ToolResultMeta | null {
  const row = providerRowIr(provider, payload, options)
  return row?.kind === 'tool' ? toolCallMeta(row) : null
}

/** The scroll-rail snippet for one frame, the way the rail itself derives it. */
export function providerRowPreviewText(provider: AgentProvider, payload: Record<string, unknown>, options: ProviderRowOptions = {}): string | null {
  const row = providerRowIr(provider, payload, options)
  if (row?.kind === 'tool')
    return toolCallMeta(row).previewText()
  const quotable = quotableTextForIR(row)
  if (quotable !== null)
    return quotable
  if (row?.kind === 'divider')
    return row.divider.label.trim() || null
  return null
}

/** Every image one frame carries, in the order an image tab addresses them. */
export function providerRowImages(provider: AgentProvider, payload: Record<string, unknown>, options: ProviderRowOptions = {}): ImageResultSource[] {
  return imagesForIR(providerRowIr(provider, payload, options))
}

/** The text Quote and Copy-Markdown write for one frame. */
export function providerQuotableText(provider: AgentProvider, payload: Record<string, unknown>, options: ProviderRowOptions = {}): string | null {
  return quotableTextForIR(providerRowIr(provider, payload, options))
}

/**
 * One provider frame, read into the shared row IR the way a mounted row reads it.
 *
 * The suite asks the same questions the app asks -- what does this row draw,
 * what can its toolbar do, which images does it carry -- and every one of them
 * is an answer about the ROW rather than a separate provider hook.
 */
export function providerRowIr(
  provider: AgentProvider,
  payload: Record<string, unknown>,
  options: ProviderRowOptions = {},
): ChatRowIR | null {
  const plugin = providerFor(provider)!
  const parsed = { ...input(payload, undefined, provider), supplementalContent: options.supplementalContent }
  const category = options.category ?? plugin?.transcript.classify(parsed)
  const sides = options.sides ?? {
    current: parsed,
    request: options.request,
    result: options.result,
    role: options.role ?? 'result',
  }
  return extractedRow(extractChatRow(provider, parsed, category, { ...options, sides }))
}

/**
 * What a test states about one row beyond the frame itself.
 *
 * `request` and `result` are the two SIDES the message store resolves for a
 * mounted row, spelled as the parsed messages a test already builds -- so a case
 * says "this result, with that request beside it" rather than assembling the
 * four-field span shape by hand.
 */
export interface ProviderRowOptions extends RowExtractionOptions {
  category?: MessageCategory
  request?: ParsedMessageContent
  result?: ParsedMessageContent
  /**
   * Where this row sits in its span. It defaults to `result`, because that is the
   * side every reader of a finished row resolves: the toolbar, the image tab and
   * the rail all address the row that carries the answer.
   */
  role?: SpanRole
  /** The LeapMux half of the message, which several providers read for a recovered body. */
  supplementalContent?: unknown
}
