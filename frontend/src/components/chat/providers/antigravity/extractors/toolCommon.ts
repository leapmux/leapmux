import { ANTIGRAVITY_STEP_STATUS, ANTIGRAVITY_STEP_TYPE, ANTIGRAVITY_TOOL } from '~/generated/contracts/antigravity-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'

export interface AntigravityStep {
  type: string
  status: string
  metadata: Record<string, unknown>
  toolCall: Record<string, unknown>
  args: Record<string, unknown>
  raw: Record<string, unknown>
}

const STEP_TYPES = new Set<string>(Object.values(ANTIGRAVITY_STEP_TYPE))

export function antigravityStepHeader(raw: Record<string, unknown> | undefined): Pick<AntigravityStep, 'type' | 'status'> | null {
  if (!raw || !isObject(raw))
    return null
  const type = pickString(raw, 'type')
  return type
    ? {
        type,
        status: pickString(raw, 'status'),
      }
    : null
}

export function antigravityStep(raw: Record<string, unknown> | undefined): AntigravityStep | null {
  const header = antigravityStepHeader(raw)
  if (!raw || !header || !STEP_TYPES.has(header.type))
    return null
  const metadata = pickObject(raw, 'metadata') ?? {}
  const toolCall = pickObject(metadata, 'toolCall') ?? {}
  let args: Record<string, unknown> = {}
  const argumentsJson = pickString(toolCall, 'argumentsJson')
  if (argumentsJson) {
    try {
      const parsed: unknown = JSON.parse(argumentsJson)
      if (!isObject(parsed))
        return null
      args = parsed
    }
    catch {
      return null
    }
  }
  return {
    ...header,
    metadata,
    toolCall,
    args,
    raw,
  }
}

const OPEN_STATUSES = new Set<string>([
  ANTIGRAVITY_STEP_STATUS.Generating,
  ANTIGRAVITY_STEP_STATUS.Queued,
  ANTIGRAVITY_STEP_STATUS.Pending,
  ANTIGRAVITY_STEP_STATUS.Running,
  ANTIGRAVITY_STEP_STATUS.Waiting,
])

export function antigravityStepOpen(step: Pick<AntigravityStep, 'status'>): boolean {
  return OPEN_STATUSES.has(step.status)
}

export function antigravityStepFailed(step: AntigravityStep): boolean {
  const exitCode = pickNumber(pickObject(step.raw, 'runCommand'), 'exitCode', undefined)
  return step.status === ANTIGRAVITY_STEP_STATUS.Error || step.status === ANTIGRAVITY_STEP_STATUS.Canceled || (exitCode !== undefined && exitCode !== 0)
}

export function antigravityStepDeclined(step: AntigravityStep): boolean {
  return antigravityStepFailed(step) && antigravityGenericResult(step).includes('user denied permission')
}

export function antigravityToolName(step: AntigravityStep): string {
  return pickString(step.toolCall, 'name')
}

export function antigravityGenericResult(step: AntigravityStep): string {
  const generic = pickObject(step.raw, 'generic')
  const result = pickObject(generic, 'result')
  const error = pickObject(step.raw, 'error')
  return pickString(result, 'result') || pickString(generic, 'resultString') || pickString(generic, 'error')
    || pickString(error, 'userErrorMessage') || pickString(error, 'modelErrorMessage') || pickString(error, 'shortError')
    || pickString(error, 'fullError') || pickString(error, 'details')
}

export function antigravityMessageIsChildPrompt(message: Record<string, unknown> | null | undefined): boolean {
  const tool = pickObject(pickObject(pickObject(message, 'sourceMetadata'), 'tool'), 'toolCall')
  return pickString(tool, 'name') === ANTIGRAVITY_TOOL.InvokeSubagent
}
