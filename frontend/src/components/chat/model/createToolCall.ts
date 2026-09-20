import type { McpContentItem } from './mcpToolCall'
import type { ToolCall, ToolCallEnvelope, ToolCallFault, ToolCallSpecVariant, ToolIconHint } from './toolCall'
import type { ToolCallStatus } from './toolCallStatus'
import type { ToolKind } from './toolKind'
import type { ToolMetadataEntry } from './toolMetadata'
import type { ToolRequestByKind } from './tools'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { failedResult, isFileChangeKind, isGenericKind, isProseResult, isProseResultKind, isToolFailureResult, isUnparsedToolResult, unparsedResult } from './toolCall'
import { deriveToolCallStatus } from './toolCallLifecycle'
import { isFinishedToolCallStatus } from './toolCallStatus'
import { isGenericToolResult } from './tools/generic'

export interface NormalizedToolCallDraft<K extends ToolKind> {
  id: string
  name: string
  kind: K
  status: ToolCallStatus
  request: ToolRequestByKind[K]
  result?: unknown
  images: readonly ImageResultSource[]
  extraContent?: readonly McpContentItem[]
  truncated?: boolean
  title?: string | undefined
  label?: string | undefined
  icon?: ToolIconHint | undefined
  metadata?: ToolMetadataEntry[] | undefined
}

export type ToolCallBuildResult<K extends ToolKind>
  = | { ok: true, call: ToolCall<K> }
    | { ok: false, fault: ToolCallFault, draft: NormalizedToolCallDraft<K> }

export interface ToolCallDraft {
  kind: ToolKind
  status: ToolCallStatus
  request: ToolRequestByKind[ToolKind]
  result?: unknown
  images: readonly ImageResultSource[]
  extraContent?: readonly McpContentItem[]
  truncated?: boolean
}

export function buildToolCall<K extends ToolKind>(envelope: ToolCallEnvelope, spec: ToolCallSpecVariant<K>): ToolCallBuildResult<K> {
  const { lifecycle, ...envelopeFields } = envelope
  const { statusOverride, name, images, extraContent, truncated, ...rest } = spec
  const draft: NormalizedToolCallDraft<K> = {
    ...envelopeFields,
    ...rest,
    name: name ?? envelope.name,
    images: images ?? [],
    status: deriveToolCallStatus(lifecycle, spec.result !== undefined, statusOverride),
    ...(extraContent !== undefined ? { extraContent } : {}),
    ...(truncated !== undefined ? { truncated } : {}),
  }
  const fault = toolCallFault(draft)
  if (fault !== null)
    return { ok: false, fault, draft }
  return { ok: true, call: draft as ToolCall<K> }
}

export function toolCallFault(draft: ToolCallDraft): ToolCallFault | null {
  const finished = isFinishedToolCallStatus(draft.status)
  if (draft.result !== undefined && !finished)
    return 'result-before-the-call-finished'
  if (!finished && (draft.images.length > 0 || draft.extraContent !== undefined || draft.truncated !== undefined))
    return 'pictures-before-the-call-finished'
  if (draft.status === 'completed' && isToolFailureResult(draft.result))
    return 'completed-with-a-failure-result'
  if (draft.status === 'failed' && isUnparsedToolResult(draft.result))
    return 'failed-with-an-unparsed-result'
  if (draft.status === 'declined' && draft.result !== undefined && !isToolFailureResult(draft.result)
    && !(isProseResultKind(draft.kind) && isProseResult(draft.result))) {
    return 'declined-with-a-typed-payload'
  }
  if (draft.status === 'declined' && (draft.images.length > 0 || draft.extraContent !== undefined || draft.truncated !== undefined))
    return 'declined-with-artifacts'
  if (draft.status === 'incomplete' && (draft.result !== undefined || draft.images.length > 0 || draft.extraContent !== undefined || draft.truncated !== undefined))
    return 'incomplete-with-artifacts'
  if (isGenericKind(draft.kind) && draft.images.length > 0)
    return 'a-generic-kind-carries-its-own-images'
  if (isFileChangeKind(draft.kind) && !statesAFile(draft.request))
    return 'a-file-change-states-no-file'
  return null
}

function statesAFile(request: ToolRequestByKind[ToolKind]): boolean {
  const changes = (request as { changes?: readonly { filePath?: string }[] }).changes
  return changes !== undefined && changes.length > 0 && changes.every(change => Boolean(change.filePath))
}

export function createToolCall<K extends ToolKind>(envelope: ToolCallEnvelope, spec: ToolCallSpecVariant<K>): ToolCall<K> | ToolCall<'other'> {
  const built = buildToolCall(envelope, spec)
  return built.ok ? built.call : degradedToolCall(built)
}

const reportedFaults = new Set<ToolCallFault>()

function warnDegraded(built: { fault: ToolCallFault, draft: { id: string, name: string, kind: ToolKind, status: ToolCallStatus } }): void {
  if (reportedFaults.has(built.fault))
    return
  reportedFaults.add(built.fault)
  console.warn('ToolCall degraded to the uncategorized row', {
    fault: built.fault,
    callId: built.draft.id,
    toolName: built.draft.name,
    originalKind: built.draft.kind,
    status: built.draft.status,
  })
}

export function __resetToolCallWarningsForTest(): void {
  reportedFaults.clear()
}

function degradedToolCall<K extends ToolKind>(built: { fault: ToolCallFault, draft: NormalizedToolCallDraft<K> }): ToolCall<'other'> {
  warnDegraded(built)
  const { draft } = built
  const status = draft.status
  const finished = isFinishedToolCallStatus(status)
  const kept = isGenericToolResult(draft.result) ? draft.result : undefined
  const text = faultText(draft, built.fault)
  const common = {
    id: draft.id,
    name: draft.name,
    status,
    kind: 'other' as const,
    request: { args: requestArgs(draft.request) },
    ...(draft.title !== undefined ? { title: draft.title } : {}),
    ...(draft.label !== undefined ? { label: draft.label } : {}),
    ...(draft.icon !== undefined ? { icon: draft.icon } : {}),
    ...(draft.metadata !== undefined ? { metadata: draft.metadata } : {}),
    degradation: { fault: built.fault, originalKind: draft.kind },
  }
  if (!finished)
    return { ...common, status: status as 'unstated' | 'pending' | 'in_progress', images: [] }
  if (status === 'completed')
    return { ...common, status, result: kept ?? unparsedResult(text), images: [] }
  if (status === 'incomplete')
    return { ...common, status, images: [] }
  if (status === 'declined')
    return { ...common, status, result: failedResult(text), images: [] }
  return { ...common, status, result: kept ?? failedResult(text), images: [] }
}

function faultText(draft: { result?: unknown }, fault: ToolCallFault): string {
  const result = draft.result
  if (result !== undefined && typeof (result as { text?: unknown }).text === 'string')
    return (result as { text: string }).text
  return `This build could not read the call: ${fault.replaceAll('-', ' ')}.`
}

function requestArgs(request: ToolRequestByKind[ToolKind]): Record<string, unknown> {
  const args = (request as { args?: unknown }).args
  return typeof args === 'object' && args !== null && !Array.isArray(args)
    ? args as Record<string, unknown>
    : { ...request as object }
}
