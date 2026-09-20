import type { McpContentItem } from './mcpToolCall'
import type { ToolCallStatus } from './toolCallStatus'
import type { ToolKind } from './toolKind'
import type { ToolMetadataEntry } from './toolMetadata'
import type { ProviderToolOutcome, RetainedToolOutcome } from './toolOutcome'
import type { ToolRequestByKind, ToolResultByKind } from './tools'
import type { ImageResultSource } from '~/lib/imageBlocks'

export type ToolIconHint
  = | 'checklist'
    | 'stop'
    | 'plan-enter'
    | 'plan-exit'
    | 'webhook'
    | 'branch'
    | 'json'

export interface ProseResult { text: string, format: 'plain' | 'markdown' }
export interface UnparsedToolResult { readonly unparsed: true, text: string }
export interface ToolFailureResult { readonly failure: true, text: string }

export type ToolResult<K extends ToolKind> = ToolResultByKind[K] | ToolFailureResult | UnparsedToolResult
export type GenericToolKind = 'unspecified' | 'other' | 'mcp'
export type ToolCallImages<K extends ToolKind> = K extends GenericToolKind ? readonly [] : readonly ImageResultSource[]

export interface ToolCallResultBase<K extends ToolKind> {
  images: ToolCallImages<K>
  extraContent?: readonly McpContentItem[]
  truncated?: boolean
}

export interface UnfinishedToolCallState {
  status: 'unstated' | 'pending' | 'in_progress'
  result?: undefined
  images: readonly []
  extraContent?: undefined
  truncated?: undefined
}

export interface CompletedToolCallState<K extends ToolKind> extends ToolCallResultBase<K> {
  status: 'completed'
  result: ToolResultByKind[K] | UnparsedToolResult
}

export interface FailedToolCallState<K extends ToolKind> extends ToolCallResultBase<K> {
  status: 'failed'
  result?: ToolResultByKind[K] | ToolFailureResult
}

export interface DeclinedToolCallState<K extends ToolKind> {
  status: 'declined'
  result?: Extract<ToolResultByKind[K], ProseResult> | ToolFailureResult
  images: readonly []
  extraContent?: undefined
  truncated?: undefined
}

export interface CancelledToolCallState<K extends ToolKind> extends ToolCallResultBase<K> {
  status: 'cancelled'
  result?: ToolResult<K>
}

export interface IncompleteToolCallState {
  status: 'incomplete'
  result?: undefined
  images: readonly []
  extraContent?: undefined
  truncated?: undefined
}

export type ToolCallLifecycle<K extends ToolKind>
  = | UnfinishedToolCallState
    | CompletedToolCallState<K>
    | FailedToolCallState<K>
    | DeclinedToolCallState<K>
    | CancelledToolCallState<K>
    | IncompleteToolCallState

export type ToolCallFault
  = | 'result-before-the-call-finished'
    | 'pictures-before-the-call-finished'
    | 'completed-with-a-failure-result'
    | 'failed-with-an-unparsed-result'
    | 'declined-with-a-typed-payload'
    | 'declined-with-artifacts'
    | 'incomplete-with-artifacts'
    | 'a-generic-kind-carries-its-own-images'
    | 'a-file-change-states-no-file'

export interface ToolCallDegradation {
  fault: ToolCallFault
  originalKind: ToolKind
}

export interface ToolCallBase {
  id: string
  name: string
  title?: string
  label?: string
  icon?: ToolIconHint
  metadata?: ToolMetadataEntry[]
  degradation?: ToolCallDegradation
}

export type ToolCallVariant<K extends ToolKind> = ToolCallBase & { kind: K, request: ToolRequestByKind[K] } & ToolCallLifecycle<K>
export type ToolCall<K extends ToolKind = ToolKind> = K extends ToolKind ? ToolCallVariant<K> : never

export type ToolCallSpecVariant<K extends ToolKind>
  = { kind: K, request: ToolRequestByKind[K], result?: ToolResult<K> }
    & Partial<Pick<ToolCallBase, 'name' | 'label' | 'icon' | 'metadata'>>
    & { title?: string | undefined }
    & {
      images?: readonly ImageResultSource[]
      extraContent?: readonly McpContentItem[]
      truncated?: boolean
      statusOverride?: 'completed' | 'failed' | 'cancelled' | 'declined'
    }

export type ToolCallSpec<K extends ToolKind = ToolKind> = K extends ToolKind ? ToolCallSpecVariant<K> : never

/** A specification reader whose result stays correlated with one tool kind. */
export type ToolCallSpecReader<Facts, K extends ToolKind> = (facts: Facts) => ToolCallSpecVariant<K>

/** One correlated specification reader for each tool kind. */
export type ToolCallSpecReaderTable<Facts> = {
  [K in ToolKind]: ToolCallSpecReader<Facts, K>
}

/** Read one specification while preserving the correlation between its kind and data. */
export function readToolCallSpec<K extends ToolKind, Facts>(
  readers: ToolCallSpecReaderTable<Facts>,
  kind: K,
  facts: Facts,
): ToolCallSpecVariant<K> {
  return readers[kind](facts)
}

export interface ToolCallLifecycleFacts {
  frameStatus: ToolCallStatus
  providerOutcome: ProviderToolOutcome | null
  retainedOutcome: RetainedToolOutcome | null
  rowFinal: boolean
  resultFrameLanded: boolean
}

export type ToolCallEnvelope = Pick<ToolCallBase, 'id' | 'name'> & { lifecycle: ToolCallLifecycleFacts }

export const FILE_CHANGE_KINDS = ['edit', 'write', 'delete', 'move'] as const
export type FileChangeKind = (typeof FILE_CHANGE_KINDS)[number]
const FILE_CHANGE_KIND_SET: ReadonlySet<string> = new Set(FILE_CHANGE_KINDS)

export function isFileChangeKind(kind: ToolKind): kind is FileChangeKind {
  return FILE_CHANGE_KIND_SET.has(kind)
}

export const PROSE_RESULT_KINDS = [
  'agents',
  'memory',
  'message',
  'report',
  'skill',
  'switch_mode',
  'think',
  'trigger',
  'wait',
] as const

export type ProseResultKind = (typeof PROSE_RESULT_KINDS)[number]
const PROSE_RESULT_KIND_SET: ReadonlySet<string> = new Set(PROSE_RESULT_KINDS)

export function isProseResultKind(kind: ToolKind): kind is ProseResultKind {
  return PROSE_RESULT_KIND_SET.has(kind)
}

export function unparsedResult(text: string): UnparsedToolResult {
  return { unparsed: true, text }
}

export function failedResult(text: string): ToolFailureResult {
  return { failure: true, text }
}

export function proseResult(text: string, format: ProseResult['format'] = 'plain'): ProseResult {
  return { text, format }
}

export function isUnparsedToolResult(result: unknown): result is UnparsedToolResult {
  return typeof result === 'object' && result !== null
    && (result as { unparsed?: unknown }).unparsed === true
    && typeof (result as { text?: unknown }).text === 'string'
}

export function isToolFailureResult(result: unknown): result is ToolFailureResult {
  return typeof result === 'object' && result !== null
    && (result as { failure?: unknown }).failure === true
    && typeof (result as { text?: unknown }).text === 'string'
}

export function isProseResult(result: unknown): result is ProseResult {
  if (typeof result !== 'object' || result === null || isUnparsedToolResult(result) || isToolFailureResult(result))
    return false
  const format = (result as { format?: unknown }).format
  return typeof (result as { text?: unknown }).text === 'string' && (format === 'plain' || format === 'markdown')
}

export function typedResult<K extends ToolKind>(call: { kind: K, result?: ToolResult<NoInfer<K>> | undefined }): ToolResultByKind[K] | undefined {
  const result = call.result
  return result === undefined || isUnparsedToolResult(result) || isToolFailureResult(result) ? undefined : result
}

export function isGenericKind(kind: ToolKind): kind is GenericToolKind {
  return kind === 'unspecified' || kind === 'other' || kind === 'mcp'
}

export function isGenericCall(call: ToolCall): call is ToolCall<GenericToolKind> {
  return isGenericKind(call.kind)
}
