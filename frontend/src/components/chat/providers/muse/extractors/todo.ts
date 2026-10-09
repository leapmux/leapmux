import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { TodoItem } from '~/models/todo'
import { isObject } from '~/lib/jsonPick'
import { todoRowKey } from '~/models/todo'
import { failedResult, unparsedResult } from '../../../model/toolCall'

const MODEL_TODO_STATUSES: ReadonlyMap<string, TodoItem['status']> = new Map([
  ['pending', 'pending'],
  ['in_progress', 'in_progress'],
  ['completed', 'completed'],
  ['cancelled', 'deleted'],
])

export interface MuseTodoFacts {
  args: Record<string, unknown>
  hasResult: boolean
  failed: boolean
  output: string
}

function requestedTodos(value: unknown): TodoItem[] | null {
  if (!Array.isArray(value))
    return null
  const items: TodoItem[] = []
  for (const [index, entry] of value.entries()) {
    if (!isObject(entry) || typeof entry.text !== 'string' || entry.text.trim() === '' || typeof entry.status !== 'string')
      return null
    const status = MODEL_TODO_STATUSES.get(entry.status)
    if (status === undefined)
      return null
    items.push({ rowKey: todoRowKey(undefined, index, entry.text), content: entry.text, status, activeForm: '' })
  }
  return items
}

/** Read model arguments separately from the native list event and the actual tool result. */
export function museTodoSpec(facts: MuseTodoFacts): ToolCallSpecVariant<'todo'> | ToolCallSpecVariant<'other'> {
  const items = requestedTodos(facts.args.todos)
  if (items === null) {
    return {
      kind: 'other',
      request: { args: facts.args },
      metadata: [{ label: 'Native to-do request', value: 'This build could not read its task list.' }],
      ...(facts.hasResult ? { result: facts.failed ? failedResult(facts.output) : unparsedResult(facts.output) } : {}),
    }
  }
  return {
    kind: 'todo',
    request: { items },
    ...(facts.hasResult ? { result: facts.failed ? failedResult(facts.output) : { items, note: facts.output } } : {}),
  }
}
