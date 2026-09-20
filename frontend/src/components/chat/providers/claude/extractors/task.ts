import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { ToolMetadataEntry } from '../../../model/toolMetadata'
import type { TaskRequest, TaskStatus } from '../../../model/tools/task'
import type { ClaudeToolRow } from './toolCommon'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { unparsedResult } from '../../../model/toolCall'
import { formatTaskStatus, joinMetaParts } from '../../../rendererUtils'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeToolFailureResult } from './failure'

/** The state a background task reports, in the four words the status body draws. */
function claudeTaskStatus(status: string): TaskStatus {
  if (status === 'completed')
    return 'completed'
  if (status === 'failed' || status === 'error')
    return 'failed'
  if (status === 'killed' || status === 'stopped' || status === 'cancelled')
    return 'stopped'
  return 'running'
}

/**
 * The task pair: which background task the call acted on, and the state it
 * reported. `TaskOutput` reads a task; `TaskStop` ends one.
 *
 * The failure rung leads, as it does in every kind whose failed call states text
 * alone. A failed task call carries NO `tool_use_result`, so its reason reached the
 * two rungs below: `TaskOutput` answered `unparsedResult`, which claims the call
 * completed and contradicts the row's own failed status, and `TaskStop` drew the error
 * SENTENCE as the title of the task it stopped.
 */
export function claudeTaskSpec(request: TaskRequest, args: ClaudeToolRow, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'task'> {
  const toolName = args.toolName
  // Both rows of the span share one call, so a title set only on the no-result
  // branch disappears from BOTH headers the moment the result lands -- and a
  // transcript can hold several concurrent background tasks with nothing else to
  // tell them apart.
  const title = claudeTaskTitle(args, request)
  if (!result)
    return { kind: 'task', request, title }
  const failure = claudeToolFailureResult(result)
  if (failure)
    return { kind: 'task', request, title, result: failure }
  if (toolName === CLAUDE_TOOL_NAMES.TASK_OUTPUT) {
    const task = pickObject(result.toolUseResult, 'task')
    if (!task || !isObject(task))
      return { kind: 'task', request, title, result: unparsedResult(result.resultContent) }
    const status = pickString(task, 'status')
    const exitCode = pickNumber(task, 'exitCode')
    const meta = joinMetaParts([
      pickString(task, 'task_id') && `task ID: ${pickString(task, 'task_id')}`,
      exitCode !== null && `exit code: ${exitCode}`,
    ])
    const label = formatTaskStatus(status || undefined)
    const description = pickString(task, 'description')
    const head = label && description ? `${label}: ${description}` : (label || description || CLAUDE_TOOL_NAMES.TASK_OUTPUT)
    return {
      kind: 'task',
      request,
      title,
      result: {
        title: meta ? `${head} (${meta})` : head,
        outcome: claudeTaskStatus(status),
        output: pickString(task, 'output', result.resultContent),
      },
    }
  }
  const message = pickString(result.toolUseResult, 'message') || result.resultContent
  if (!message)
    return { kind: 'task', request, title, result: unparsedResult(result.resultContent) }
  const taskType = pickString(result.toolUseResult, 'task_type')
  const metadata: ToolMetadataEntry[] = taskType ? [{ label: 'Task type', value: taskType }] : []
  const command = pickString(result.toolUseResult, 'command')
  return {
    kind: 'task',
    request,
    title,
    metadata,
    result: {
      title: taskType ? `${message} (${taskType})` : message,
      // A stop that reaches this rung ENDED the task. The failure rung above already
      // answered every call the tool marked failed, so no other outcome is reachable.
      outcome: 'stopped',
      // The command rides only when the record stated one.
      ...(command ? { command } : {}),
      output: '',
    },
  }
}

/** The header word while the call runs: what it waits for, or what it stops. */
function claudeTaskTitle(args: ClaudeToolRow, request: TaskRequest): string {
  if (request.action === 'output') {
    const timeout = pickNumber(args.input, 'timeout')
    const inner = joinMetaParts([
      request.taskId && `task ID: ${request.taskId}`,
      timeout !== null && `timeout: ${timeout >= 1000 ? `${timeout / 1000}s` : `${timeout}ms`}`,
      args.input.block !== undefined ? `block: ${String(args.input.block)}` : '',
    ])
    return inner ? `Waiting for output (${inner})` : 'Waiting for output'
  }
  return request.taskId ? `Stop task ${request.taskId}` : 'Stop task'
}
