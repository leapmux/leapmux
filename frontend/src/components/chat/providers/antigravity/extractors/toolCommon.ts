import { ANTIGRAVITY_STEP_STATUS, ANTIGRAVITY_STEP_TYPE } from '~/generated/contracts/antigravity-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'

export interface AntigravityStep {
  type: string
  status: string
  metadata: Record<string, unknown>
  toolCall: Record<string, unknown>
  args: Record<string, unknown>
  raw: Record<string, unknown>
}

export function antigravityStep(raw: Record<string, unknown> | undefined): AntigravityStep | null {
  if (!raw || !isObject(raw))
    return null
  const type = pickString(raw, 'type')
  if (!type)
    return null
  const metadata = pickObject(raw, 'metadata') ?? {}
  const toolCall = pickObject(metadata, 'toolCall') ?? {}
  let args: Record<string, unknown> = {}
  const argumentsJson = pickString(toolCall, 'argumentsJson')
  if (argumentsJson) {
    try {
      const parsed: unknown = JSON.parse(argumentsJson)
      if (isObject(parsed))
        args = parsed
    }
    catch {
      // The generic row retains the malformed arguments as native text.
    }
  }
  return {
    type,
    status: pickString(raw, 'status'),
    metadata,
    toolCall,
    args,
    raw,
  }
}

export function antigravityStepOpen(step: AntigravityStep): boolean {
  return [
    'CORTEX_STEP_STATUS_GENERATING',
    'CORTEX_STEP_STATUS_QUEUED',
    'CORTEX_STEP_STATUS_PENDING',
    'CORTEX_STEP_STATUS_RUNNING',
    ANTIGRAVITY_STEP_STATUS.Waiting,
  ].includes(step.status)
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

export function antigravityToolArguments(step: AntigravityStep): Record<string, unknown> {
  return step.args
}

export function antigravityGenericResult(step: AntigravityStep): string {
  const generic = pickObject(step.raw, 'generic')
  const result = pickObject(generic, 'result')
  const error = pickObject(step.raw, 'error')
  return pickString(result, 'result') || pickString(generic, 'resultString') || pickString(generic, 'error')
    || pickString(error, 'userErrorMessage') || pickString(error, 'modelErrorMessage') || pickString(error, 'shortError')
    || pickString(error, 'fullError') || pickString(error, 'details')
}

export function antigravityStepIsQuestion(step: AntigravityStep): boolean {
  return step.type === ANTIGRAVITY_STEP_TYPE.AskQuestion
}
