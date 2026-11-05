import type { ToolCall, ToolCallEnvelope, ToolCallSpecReaderTable, ToolCallSpecVariant, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolCallStatus } from '../../../model/toolCallStatus'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { AntigravityStep } from './toolCommon'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { ANTIGRAVITY_STEP_STATUS } from '~/generated/contracts/antigravity-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { failedResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { toolRequestFor } from '../../defaultToolRequests'
import { questionsFromRecords } from '../../questionRecords'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { antigravityToolKind } from '../toolKinds'
import { antigravityAgentRequest, antigravityAgentResult } from './agent'
import { antigravityCommandResult, antigravityExecuteRequest } from './execute'
import { antigravityFileChange, antigravityLandedChanges } from './fileEdit'
import { antigravityMcpRequest, antigravityMcpResult } from './mcp'
import { antigravityReadImages, antigravityReadRequest, antigravityReadResult } from './read'
import { antigravityGenericResult, antigravityStep, antigravityStepDeclined, antigravityStepFailed, antigravityStepOpen, antigravityToolName } from './toolCommon'

export interface AntigravityToolRow {
  own: AntigravityStep
  result?: AntigravityStep
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

function antigravityToolFacts(step: AntigravityStep, row: AntigravityToolRow): AntigravityToolFacts {
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
  return antigravityStepOpen({ status } as AntigravityStep)
    ? 'in_progress'
    : 'unstated'
}

export const ANTIGRAVITY_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<AntigravityToolFacts> = {
  read: (_args, facts): ToolRequestByKind['read'] => antigravityReadRequest(facts.step),
  execute: (_args, facts): ToolRequestByKind['execute'] => antigravityExecuteRequest(facts.step),
  edit: (_args, facts): ToolRequestByKind['edit'] => ({
    changes: antigravityFileChange(facts.step),
  }),
  write: (_args, facts): ToolRequestByKind['write'] => ({
    changes: antigravityFileChange(facts.step),
  }),
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
  question: (args): ToolRequestByKind['question'] => ({
    questions: questionsFromRecords(args.questions, question => ({
      question: pickString(question, 'question'),
    }), option => ({
      label: pickString(option, 'text'),
    })),
  }),
}

function requestFor<K extends ToolKind>(kind: K, facts: AntigravityToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.args, facts, ANTIGRAVITY_TOOL_REQUEST_OVERRIDES)
}

function unread(facts: AntigravityToolFacts): UnparsedToolResult | undefined {
  return facts.text
    ? unparsedResult(facts.text)
    : undefined
}

function early<K extends ToolKind>(kind: K, facts: AntigravityToolFacts, request: ToolRequestByKind[K]): ToolCallSpecVariant<K> | null {
  if (!facts.answered)
    return { kind, request }
  if (facts.failed) {
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
    const result = unread(facts)
    return {
      kind,
      request,
      ...(result
        ? { result }
        : {}),
    }
  }
}

export const ANTIGRAVITY_TOOL_READERS: ToolCallSpecReaderTable<AntigravityToolFacts> = {
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const request = requestFor('execute', facts)
    const initial = early('execute', facts, request)
    const exitCode = pickNumber(pickObject(facts.step.raw, 'runCommand'), 'exitCode', undefined)
    if (initial && (exitCode === undefined || exitCode === 0))
      return initial
    return {
      kind: 'execute',
      request,
      result: {
        commands: [
          antigravityCommandResult(facts.step),
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
      result: antigravityReadResult(facts.step),
      images: antigravityReadImages(facts.step),
    }
  },
  edit: (facts): ToolCallSpecVariant<'edit'> => {
    const request = requestFor('edit', facts)
    return early('edit', facts, request) ?? {
      kind: 'edit',
      request,
      result: { changes: antigravityLandedChanges(facts.step) ?? request.changes },
    }
  },
  write: (facts): ToolCallSpecVariant<'write'> => {
    const request = requestFor('write', facts)
    return early('write', facts, request) ?? {
      kind: 'write',
      request,
      result: { changes: antigravityLandedChanges(facts.step) ?? request.changes },
    }
  },
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = requestFor('agent', facts)
    const initial = early('agent', facts, request)
    if (initial)
      return { ...initial, title: request.description }
    return {
      kind: 'agent',
      request,
      title: request.description,
      result: antigravityAgentResult(facts.step, request),
    }
  },
  mcp: (facts): ToolCallSpecVariant<'mcp'> => {
    const request = requestFor('mcp', facts)
    return early('mcp', facts, request) ?? {
      kind: 'mcp',
      request,
      result: antigravityMcpResult(facts.step),
    }
  },
  question: declared('question'),
  web_search: declared('web_search'),
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

export function antigravityToolRow(raw: Record<string, unknown> | undefined, result: Record<string, unknown> | undefined, completion?: MessageCompletion): AntigravityToolRow | null {
  const own = antigravityStep(raw)
  if (!own)
    return null
  const other = antigravityStep(result)
  const ownID = pickString(own.toolCall, 'id')
  const paired = other && pickString(other.toolCall, 'id') === ownID
    ? other
    : undefined
  return {
    own,
    ...(paired
      ? { result: paired }
      : {}),
    ...(completion !== undefined
      ? { completion }
      : {}),
    rowFinal: retainedRowIsFinal(completion) || !antigravityStepOpen(own),
  }
}

export function antigravityStepResultText(step: AntigravityStep): string {
  return antigravityGenericResult(step) || pickString(pickObject(step.raw, 'runCommand'), 'commandLine')
}

function antigravityToolCall(row: AntigravityToolRow): ToolCall {
  const step = row.result ?? row.own
  const facts = antigravityToolFacts(step, row)
  const envelope: ToolCallEnvelope = {
    id: pickString(step.toolCall, 'id') || `step-${String(step.raw.stepIndex ?? '')}`,
    name: antigravityToolName(step),
    lifecycle: facts.lifecycle,
  }
  return createToolCall(envelope, readToolCallSpec(ANTIGRAVITY_TOOL_READERS, antigravityToolKind(envelope.name), facts))
}

export { antigravityToolCall }
