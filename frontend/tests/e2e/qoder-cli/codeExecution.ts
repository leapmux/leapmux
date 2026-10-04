import { isObject } from '../../../src/lib/jsonPick'
import { nativeXmlField } from '../helpers/nativeXml'

/** Keep exact Worker sequence and revision values in native diagnostic attachments. */
export function qoderWorkflowDiagnosticJson(value: unknown): string {
  const text = JSON.stringify(value, (_, field) => typeof field === 'bigint' ? field.toString() : field, 2)
  if (text === undefined)
    throw new Error('The Qoder Workflow diagnostic value cannot be serialized.')
  return text
}

export interface QoderWorkflowLaunch {
  sessionId: string
  callId: string
  taskId: string
  runId: string
  transcriptDir: string
  scriptPath: string
}

export interface QoderWorkflowModelOutcome {
  status: 'completed' | 'failed'
  summary: string
  outputFile: string
  result?: unknown
}

/** Read Qoder's actual user notification for the original task and call. */
export function qoderWorkflowModelOutcome(values: readonly unknown[], launch: QoderWorkflowLaunch): QoderWorkflowModelOutcome | undefined {
  const outcomes: QoderWorkflowModelOutcome[] = []
  for (const value of values) {
    if (!isObject(value) || !Array.isArray(value.messages))
      continue
    for (const message of value.messages) {
      if (!isObject(message) || message.role !== 'user')
        continue
      const texts = typeof message.content === 'string'
        ? [message.content]
        : Array.isArray(message.content)
          ? message.content.flatMap(block => isObject(block) && block.type === 'text' && typeof block.text === 'string' ? [block.text] : [])
          : []
      for (const text of texts) {
        for (const match of text.matchAll(/<task-notification>\n([\s\S]*?)\n<\/task-notification>/g)) {
          const body = match[1]!
          if (nativeXmlField(body, 'task-id') !== launch.taskId || nativeXmlField(body, 'tool-use-id') !== launch.callId)
            continue
          const status = nativeXmlField(body, 'status')
          const summary = nativeXmlField(body, 'summary')
          const outputFile = nativeXmlField(body, 'output-file')
          if ((status !== 'completed' && status !== 'failed') || summary === undefined || !outputFile)
            throw new Error('The Qoder Workflow notification has no complete final native outcome.')
          const resultText = nativeXmlField(body, 'result')
          outcomes.push({ status, summary, outputFile, ...(resultText === undefined ? {} : { result: resultText }) })
        }
      }
    }
  }
  const first = outcomes[0]
  if (outcomes.some(outcome => JSON.stringify(outcome) !== JSON.stringify(first)))
    throw new Error('The Qoder Workflow repeats a different final outcome for the same task.')
  return first
}

/** Read Qoder's exact native launch wrapper without treating its status as completion. */
export function qoderWorkflowLaunch(value: unknown, callId: string): QoderWorkflowLaunch | undefined {
  if (!isObject(value) || typeof value.session_id !== 'string' || value.session_id === '' || !isObject(value.message) || !Array.isArray(value.message.content)
    || !isObject(value.tool_use_result) || typeof value.tool_use_result.payload !== 'string') {
    return undefined
  }
  const results = value.message.content.filter(isObject).filter(block => block.type === 'tool_result' && block.tool_use_id === callId)
  if (results.length !== 1)
    return undefined
  const payload: unknown = JSON.parse(value.tool_use_result.payload)
  if (!isObject(payload) || payload.status !== 'async_launched' || typeof payload.taskId !== 'string' || !/^wf-[\w-]+$/.test(payload.taskId)
    || typeof payload.runId !== 'string' || !/^wf_[\w-]+$/.test(payload.runId) || typeof payload.transcriptDir !== 'string'
    || payload.transcriptDir === '' || typeof payload.scriptPath !== 'string' || payload.scriptPath === '') {
    return undefined
  }
  return { sessionId: value.session_id, callId, taskId: payload.taskId, runId: payload.runId, transcriptDir: payload.transcriptDir, scriptPath: payload.scriptPath }
}

/** Validate the complete native output against the exact launched workflow. */
export function qoderWorkflowOutput(value: unknown, launch: QoderWorkflowLaunch): { status: 'completed' | 'failed', result?: unknown, error?: string } {
  if (!isObject(value) || value.runId !== launch.runId || value.taskId !== launch.taskId || (value.status !== 'completed' && value.status !== 'failed'))
    throw new Error('The Qoder Workflow output has no matching final run identity.')
  if (value.status === 'failed') {
    if (typeof value.error !== 'string' || !value.error)
      throw new Error('The failed Qoder Workflow output has no native error.')
    return { status: 'failed', error: value.error }
  }
  return { status: 'completed', ...('result' in value ? { result: value.result } : {}) }
}
