import { isObject } from '../../../src/lib/jsonPick'
import { nativeXmlField } from '../helpers/nativeXml'

/** Read the return value that native Claude stores in a completed task output. */
export function claudeWorkflowOutput(value: unknown): { result?: unknown } {
  if (!isObject(value))
    throw new Error('The Claude Workflow output is not a native JSON object.')
  return { ...('result' in value ? { result: value.result } : {}) }
}

/** Failed tasks leave diagnostic bytes. Completed tasks require their native output JSON. */
export function claudeWorkflowOutputFile(text: string, status: 'completed' | 'failed'): { result?: unknown } {
  if (status === 'failed')
    return {}
  if (text === '')
    throw new Error('The completed Claude Workflow full tool output is empty.')
  const value: unknown = JSON.parse(text)
  return claudeWorkflowOutput(value)
}

export interface ClaudeWorkflowLaunch {
  callId: string
  taskId: string
  runId: string
  transcriptDir?: string
  scriptPath?: string
}

export interface ClaudeWorkflowModelOutcome {
  status: 'completed' | 'failed'
  summary: string
  outputFile: string
  result?: unknown
}

/** Read actual user notifications. Assistant source and tool schemas cannot prove completion. */
export function claudeWorkflowModelOutcome(values: readonly unknown[], launch: ClaudeWorkflowLaunch): ClaudeWorkflowModelOutcome | undefined {
  const outcomes: ClaudeWorkflowModelOutcome[] = []
  for (const value of values) {
    if (!isObject(value) || !Array.isArray(value.messages))
      continue
    for (const message of value.messages) {
      if (!isObject(message) || message.role !== 'user' || !Array.isArray(message.content))
        continue
      for (const block of message.content) {
        if (!isObject(block) || block.type !== 'text' || typeof block.text !== 'string')
          continue
        for (const match of block.text.matchAll(/<task-notification>\n([\s\S]*?)\n<\/task-notification>/g)) {
          const body = match[1]!
          if (nativeXmlField(body, 'task-id') !== launch.taskId || nativeXmlField(body, 'tool-use-id') !== launch.callId)
            continue
          const status = nativeXmlField(body, 'status')
          const summary = nativeXmlField(body, 'summary')
          const outputFile = nativeXmlField(body, 'output-file')
          if ((status !== 'completed' && status !== 'failed') || summary === undefined || !outputFile)
            throw new Error('The Claude Workflow notification has no complete final native outcome.')
          const resultText = nativeXmlField(body, 'result')
          const result: unknown = resultText === undefined ? undefined : JSON.parse(resultText)
          outcomes.push({ status, summary, outputFile, ...(resultText === undefined ? {} : { result }) })
        }
      }
    }
  }
  const first = outcomes[0]
  if (outcomes.some(outcome => JSON.stringify(outcome) !== JSON.stringify(first)))
    throw new Error('The Claude Workflow repeats a different final outcome for the same task.')
  return first
}

/** Read the native structured Workflow launch only from its exact tool result. */
export function claudeWorkflowLaunch(value: unknown, callId: string): ClaudeWorkflowLaunch | undefined {
  if (!isObject(value) || !isObject(value.message) || !Array.isArray(value.message.content))
    return undefined
  const results = value.message.content.filter(isObject).filter(block => block.type === 'tool_result' && block.tool_use_id === callId)
  if (results.length !== 1)
    return undefined
  const result = value.tool_use_result
  const payload = isObject(result) && isObject(result.data) ? result.data : result
  if (!isObject(payload) || payload.status !== 'async_launched' || typeof payload.taskId !== 'string' || payload.taskId === ''
    || typeof payload.runId !== 'string' || payload.runId === '' || ('error' in payload && payload.error !== undefined)) {
    return undefined
  }
  return {
    callId,
    taskId: payload.taskId,
    runId: payload.runId,
    ...(typeof payload.transcriptDir === 'string' ? { transcriptDir: payload.transcriptDir } : {}),
    ...(typeof payload.scriptPath === 'string' ? { scriptPath: payload.scriptPath } : {}),
  }
}

/** Read the canonical snapshot for the exact launched run and task. */
export function claudeWorkflowSnapshot(value: unknown, launch: ClaudeWorkflowLaunch): { status: 'completed' | 'failed', result?: unknown, error?: string } {
  if (!isObject(value) || value.runId !== launch.runId || value.taskId !== launch.taskId
    || (value.status !== 'completed' && value.status !== 'failed')) {
    throw new Error('The Claude Workflow snapshot has no matching final run identity.')
  }
  if (value.status === 'failed') {
    if (typeof value.error !== 'string' || value.error === '')
      throw new Error('The failed Claude Workflow snapshot has no native error.')
    return { status: 'failed', error: value.error }
  }
  return { status: 'completed', ...('result' in value ? { result: value.result } : {}) }
}
