import type { ToolCall, ToolCallEnvelope, ToolCallSpecReaderTable, ToolCallSpecVariant, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolCallStatus } from '../../../model/toolCallStatus'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind, ToolResultByKind } from '../../../model/tools'
import type { FileChangeResult } from '../../../model/tools/fileChange'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { AntigravityStep } from './toolCommon'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { ANTIGRAVITY_STEP_STATUS, ANTIGRAVITY_STEP_TYPE } from '~/generated/contracts/antigravity-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { failedResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { antigravityToolKind } from '../toolKinds'
import { antigravityAgentRequest, antigravityAgentResult } from './agent'
import { antigravityCommandResult, antigravityExecuteRequest } from './execute'
import { antigravityFileChange, antigravityLandedChanges } from './fileEdit'
import { antigravityMcpRequest, antigravityMcpResult } from './mcp'
import { antigravityQuestionRequest, antigravityQuestionResult } from './question'
import { antigravityReadImages, antigravityReadRequest, antigravityReadResult } from './read'
import { antigravityGenericResult, antigravityStepDeclined, antigravityStepFailed, antigravityStepOpen, antigravityToolName } from './toolCommon'
import { antigravityWebSearchRequest, antigravityWebSearchResult } from './webSearch'

/**
 * One row's own native step.
 *
 * A row never reads another step of its span: an open step builds the request row,
 * and a finished step builds the result row with the whole call.
 */
export interface AntigravityToolRow {
  own: AntigravityStep
  registryKey?: string
  completion?: MessageCompletion
  rowFinal: boolean
}

export interface AntigravityToolFacts {
  step: AntigravityStep
  args: Record<string, unknown>
  registryKey?: string
  text: string
  answered: boolean
  failed: boolean
  lifecycle: ToolCallEnvelope['lifecycle']
}

function antigravityToolFacts(row: AntigravityToolRow): AntigravityToolFacts {
  const step = row.own
  const answered = !antigravityStepOpen(step)
  return {
    step,
    args: step.args,
    ...(row.registryKey
      ? { registryKey: row.registryKey }
      : {}),
    text: antigravityGenericResult(step),
    answered,
    failed: antigravityStepFailed(step),
    lifecycle: {
      frameStatus: antigravityFrameStatus(step.status),
      providerOutcome: antigravityStepDeclined(step)
        ? 'declined'
        : antigravityStepFailed(step)
          ? step.status === ANTIGRAVITY_STEP_STATUS.Canceled
            ? 'interrupted'
            : 'failed'
          : null,
      retainedOutcome: retainedOutcome(row.completion),
      rowFinal: row.rowFinal,
      resultFrameLanded: answered,
    },
  }
}

function antigravityFrameStatus(status: string): ToolCallStatus {
  if (status === ANTIGRAVITY_STEP_STATUS.Done)
    return 'completed'
  if (status === ANTIGRAVITY_STEP_STATUS.Error || status === ANTIGRAVITY_STEP_STATUS.Canceled)
    return 'failed'
  if (status === ANTIGRAVITY_STEP_STATUS.Waiting)
    return 'pending'
  return antigravityStepOpen({ status })
    ? 'in_progress'
    : 'unstated'
}

function fileChangeRequest(_args: Record<string, unknown>, facts: AntigravityToolFacts): ToolRequestByKind['edit'] {
  return {
    changes: antigravityFileChange(facts.step),
  }
}

export const ANTIGRAVITY_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<AntigravityToolFacts> = {
  read: (_args, facts): ToolRequestByKind['read'] => antigravityReadRequest(facts.step),
  execute: (_args, facts): ToolRequestByKind['execute'] => antigravityExecuteRequest(facts.step),
  edit: fileChangeRequest,
  write: fileChangeRequest,
  agent: (_args, facts): ToolRequestByKind['agent'] => {
    const request = antigravityAgentRequest(facts.step)
    return {
      ...request,
      ...(facts.registryKey
        ? { registryKey: facts.registryKey }
        : request.registryKey !== undefined
          ? { registryKey: request.registryKey }
          : {}),
    }
  },
  mcp: (_args, facts): ToolRequestByKind['mcp'] => antigravityMcpRequest(facts.step),
  message: (args): ToolRequestByKind['message'] => ({
    ...(pickString(args, 'Recipient')
      ? {
          to: pickString(args, 'Recipient'),
        }
      : {}),
    text: pickString(args, 'Message'),
  }),
  question: (_args, facts): ToolRequestByKind['question'] => antigravityQuestionRequest(facts.step),
  web_search: (_args, facts): ToolRequestByKind['web_search'] => antigravityWebSearchRequest(facts.step),
}

function requestFor<K extends ToolKind>(kind: K, facts: AntigravityToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.args, facts, ANTIGRAVITY_TOOL_REQUEST_OVERRIDES)
}

function early<K extends ToolKind>(kind: K, facts: AntigravityToolFacts, request: ToolRequestByKind[K]): ToolCallSpecVariant<K> | null {
  if (!facts.answered)
    return { kind, request }
  if (facts.failed) {
    if (facts.lifecycle.providerOutcome === 'interrupted' && !facts.text)
      return { kind, request }
    return {
      kind,
      request,
      ...(antigravityStepDeclined(facts.step)
        ? { statusOverride: 'declined' as const, images: [] }
        : {}),
      result: failedResult(facts.text || 'The Antigravity step failed.'),
    }
  }
  return null
}

function declared<K extends ToolKind>(kind: K): (facts: AntigravityToolFacts) => ToolCallSpecVariant<K> {
  return (facts): ToolCallSpecVariant<K> => {
    const request = requestFor(kind, facts)
    const initial = early(kind, facts, request)
    if (initial)
      return initial
    const result = facts.text
      ? unparsedResult(facts.text)
      : undefined
    return {
      kind,
      request,
      ...(result
        ? { result }
        : {}),
    }
  }
}

function genericFallback(facts: AntigravityToolFacts, payload: string): UnparsedToolResult | undefined {
  return facts.text && !pickObject(facts.step.raw, payload)
    ? unparsedResult(facts.text)
    : undefined
}

function structuredReader<K extends ToolKind>(
  kind: K,
  payload: string,
  readResult: (step: AntigravityStep) => ToolResultByKind[K],
): (facts: AntigravityToolFacts) => ToolCallSpecVariant<K> {
  return (facts): ToolCallSpecVariant<K> => {
    const request = requestFor(kind, facts)
    return early(kind, facts, request) ?? {
      kind,
      request,
      result: genericFallback(facts, payload) ?? readResult(facts.step),
    }
  }
}

function fileChangeReader<K extends 'edit' | 'write'>(kind: K): (facts: AntigravityToolFacts) => ToolCallSpecVariant<K> {
  return (facts): ToolCallSpecVariant<K> => {
    const request = requestFor(kind, facts)
    const initial = early(kind, facts, request)
    if (initial)
      return initial
    const result: FileChangeResult | UnparsedToolResult = genericFallback(facts, 'codeAction') ?? { changes: antigravityLandedChanges(facts.step) ?? request.changes }
    return { kind, request, result }
  }
}

/**
 * The command of a step, and where the run states its working directory.
 *
 * Only a FINISHED step states the working directory: the open step that the request row
 * reads states none. So the finished step states it as a fact of the result, which the
 * row that draws the result draws, and not in its request. A result row beside its
 * request row draws no request summary, and a request field would reach no reader.
 */
function executeParts(facts: AntigravityToolFacts): Pick<ToolCallSpecVariant<'execute'>, 'request' | 'resultMetadata'> {
  const request = requestFor('execute', facts)
  if (antigravityStepOpen(facts.step) || request.cwd === undefined)
    return { request }
  const { cwd, ...asked } = request
  return {
    request: asked,
    resultMetadata: [
      { label: 'Working directory', value: cwd, path: true },
    ],
  }
}

export const ANTIGRAVITY_TOOL_READERS: ToolCallSpecReaderTable<AntigravityToolFacts> = {
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const parts = executeParts(facts)
    const initial = early('execute', facts, parts.request)
    const command = antigravityCommandResult(facts.step)
    const interrupted = facts.lifecycle.providerOutcome === 'interrupted' || facts.lifecycle.retainedOutcome === 'interrupted'
    const hasInterruptedOutput = interrupted && !!command.output && !facts.text
    if (initial && (command.exitCode === undefined || command.exitCode === 0) && !hasInterruptedOutput) {
      return {
        ...initial,
        ...(parts.resultMetadata !== undefined
          ? { resultMetadata: parts.resultMetadata }
          : {}),
      }
    }
    return {
      kind: 'execute',
      ...parts,
      result: genericFallback(facts, 'runCommand') ?? {
        commands: [
          command,
        ],
        unresolvedTerminals: [],
      },
    }
  },
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = requestFor('read', facts)
    return early('read', facts, request) ?? {
      kind: 'read',
      request,
      result: genericFallback(facts, 'viewFile') ?? antigravityReadResult(facts.step),
      images: antigravityReadImages(facts.step),
    }
  },
  edit: fileChangeReader('edit'),
  write: fileChangeReader('write'),
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = requestFor('agent', facts)
    const initial = early('agent', facts, request)
    if (initial)
      return { ...initial, title: request.description }
    return {
      kind: 'agent',
      request,
      title: request.description,
      result: genericFallback(facts, 'invokeSubagent') ?? antigravityAgentResult(facts.step, request),
    }
  },
  mcp: structuredReader('mcp', 'mcpTool', antigravityMcpResult),
  question: (facts): ToolCallSpecVariant<'question'> => {
    const request = requestFor('question', facts)
    const nativeResult = antigravityQuestionResult(facts.step)
    const initial = early('question', facts, request)
    if (initial && nativeResult?.note !== 'Dismissed')
      return initial
    const result = nativeResult ?? (facts.text
      ? unparsedResult(facts.text)
      : undefined)
    return {
      kind: 'question',
      request,
      ...(result
        ? { result }
        : {}),
    }
  },
  web_search: structuredReader('web_search', 'searchWeb', antigravityWebSearchResult),
  fetch: declared('fetch'),
  grep: declared('grep'),
  search: declared('search'),
  glob: declared('glob'),
  list: declared('list'),
  task: declared('task'),
  message: declared('message'),
  trigger: declared('trigger'),
  image: declared('image'),
  agents: declared('agents'),
  other: declared('other'),
  unspecified: declared('unspecified'),
  chart: declared('chart'),
  delete: declared('delete'),
  move: declared('move'),
  report: declared('report'),
  skill: declared('skill'),
  memory: declared('memory'),
  think: declared('think'),
  wait: declared('wait'),
  switch_mode: declared('switch_mode'),
  todo: declared('todo'),
}

export function antigravityToolRow(own: AntigravityStep, completion?: MessageCompletion): AntigravityToolRow {
  return {
    own,
    ...(completion !== undefined
      ? { completion }
      : {}),
    rowFinal: retainedRowIsFinal(completion) || !antigravityStepOpen(own),
  }
}

const STEP_TYPE_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [ANTIGRAVITY_STEP_TYPE.RunCommand, 'execute'],
  [ANTIGRAVITY_STEP_TYPE.ViewFile, 'read'],
  [ANTIGRAVITY_STEP_TYPE.AskQuestion, 'question'],
  [ANTIGRAVITY_STEP_TYPE.McpTool, 'mcp'],
  [ANTIGRAVITY_STEP_TYPE.InvokeSubagent, 'agent'],
  [ANTIGRAVITY_STEP_TYPE.SearchWeb, 'web_search'],
])

function antigravityToolCall(row: AntigravityToolRow): ToolCall {
  const step = row.own
  const facts = antigravityToolFacts(row)
  const envelope: ToolCallEnvelope = {
    id: pickString(step.toolCall, 'id') || `step-${String(step.raw.stepIndex ?? '')}`,
    name: antigravityToolName(step),
    lifecycle: facts.lifecycle,
  }
  const namedKind = antigravityToolKind(envelope.name)
  const kind = namedKind === 'unspecified'
    ? (STEP_TYPE_KINDS.get(step.type) ?? namedKind)
    : namedKind
  return createToolCall(envelope, readToolCallSpec(ANTIGRAVITY_TOOL_READERS, kind, facts))
}

export { antigravityToolCall }
