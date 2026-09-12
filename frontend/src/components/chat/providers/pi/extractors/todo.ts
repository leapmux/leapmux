import type { ToolMetadataItem } from '../../../results/ToolMetadata'
import type { TodoListSource } from '../../../todoListMessage'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { TodoItem } from '~/stores/chatTodos'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { prettifyStructuredJson } from '~/lib/jsonFormat'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { pluralize } from '~/lib/plural'
import { todoRowKey } from '~/stores/chatTodos'
import { piExtractTool, piPairedRequest, piPairedResult } from './toolCommon'

export interface PiTodoSource {
  list: TodoListSource
  description: string
  metadata: ToolMetadataItem[]
  error?: string
}

function taskId(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

/** Reject malformed snapshots so absent or invalid data cannot appear as a cleared list. */
function todoItems(tasks: unknown): TodoItem[] | null {
  if (!Array.isArray(tasks))
    return null
  const items: TodoItem[] = []
  const seen = new Set<number>()
  for (const task of tasks) {
    if (!isObject(task) || !taskId(task.id) || seen.has(task.id) || !pickString(task, 'subject').trim())
      return null
    if (['description', 'activeForm'].some(key => task[key] != null && typeof task[key] !== 'string'))
      return null
    const status = task.status
    if (status !== 'pending' && status !== 'in_progress' && status !== 'completed' && status !== 'deleted')
      return null
    seen.add(task.id)
    const id = String(task.id)
    const content = pickString(task, 'subject')
    items.push({ id, rowKey: todoRowKey(id, items.length, content), content, status, description: pickString(task, 'description'), activeForm: pickString(task, 'activeForm') })
  }
  return items
}

/** Each rpiv-todo result carries the complete saved snapshot and its operation arguments. */
export function piTodoSource(payload: Record<string, unknown>, request?: ParsedMessageContent, result?: ParsedMessageContent): PiTodoSource | null {
  const tool = piExtractTool(payload)
  if (tool?.toolName !== PI_TOOL.Todo)
    return null
  const paired = piExtractTool(piPairedResult(payload, result)?.parentObject)
  const output = paired?.result ?? tool.result
  const details = output?.details
  const args = piExtractTool(piPairedRequest(payload, request)?.parentObject)?.args ?? pickObject(details, 'params') ?? tool.args
  const action = pickString(args, 'action') || pickString(details, 'action')
  const tasks = todoItems(details?.tasks)
  const nextId = details?.nextId
  const id = taskId(args.id) ? args.id : action === 'create' && taskId(nextId) ? nextId - 1 : undefined
  const task = id !== undefined ? tasks?.find(task => task.id === String(id)) : undefined
  const subject = task?.content || pickString(args, 'subject') || (id !== undefined ? `Task #${id}` : 'task')
  const error = pickString(details, 'error') || (tool.isError || paired?.isError ? output?.text || 'The to-do operation failed.' : '')
  let title: string
  switch (action) {
    case 'create':
      title = `Create task: ${subject}`
      break
    case 'update':
      title = `Update task: ${subject}`
      break
    case 'get':
      title = `Get task: ${subject}`
      break
    case 'delete':
      title = `Delete task: ${subject}`
      break
    case 'clear':
      title = 'Clear to-do list'
      break
    case 'list':
      title = tasks ? pluralize(tasks.length, 'task') : 'To-do list'
      break
    default: return null
  }
  if (output && tasks === null && !error)
    return null
  const visible = action === 'list'
    ? tasks?.filter(task => (args.includeDeleted === true || task.status !== 'deleted') && (!args.status || task.status === args.status)) ?? []
    : action === 'clear' ? [] : task ? [task] : tasks ?? []
  if (action === 'list' && tasks)
    title = pluralize(visible.length, 'task')
  const metadata: ToolMetadataItem[] = []
  if (task) {
    const raw = Array.isArray(details?.tasks) ? details.tasks.find(value => isObject(value) && String(value.id) === task.id) : undefined
    if (isObject(raw)) {
      metadata.push({ label: 'Task ID', value: task.id! })
      if (pickString(raw, 'owner'))
        metadata.push({ label: 'Owner', value: pickString(raw, 'owner') })
      if (Array.isArray(raw.blockedBy) && raw.blockedBy.every(taskId) && raw.blockedBy.length)
        metadata.push({ label: 'Blocked by', value: raw.blockedBy.map(id => `#${id}`).join(', ') })
      const extra = prettifyStructuredJson(raw.metadata)
      if (extra)
        metadata.push({ label: 'Metadata', value: extra })
    }
  }
  return {
    list: { toolName: PI_TOOL.Todo, title, todos: visible, emptyText: action === 'clear' ? 'To-do list cleared' : action === 'list' ? 'No matching tasks' : 'The provider did not supply the task.' },
    description: task?.description || pickString(args, 'description'),
    metadata,
    error: error || undefined,
  }
}
