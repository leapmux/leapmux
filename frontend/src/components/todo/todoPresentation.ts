import type { TodoItem } from '~/models/todo'

function todoStatusRank(status: TodoItem['status']): number {
  switch (status) {
    case 'in_progress':
      return 0
    case 'blocked':
      return 1
    case 'pending':
      return 2
    case 'completed':
      return 3
    case 'deleted':
      return 4
  }
}

export function sortTodos(todos: TodoItem[]): TodoItem[] {
  return todos.toSorted((a, b) => todoStatusRank(a.status) - todoStatusRank(b.status))
}

export function todoDisplayLabel(todo: { status: TodoItem['status'], content: string, activeForm?: string }): string {
  if (todo.status === 'in_progress' && todo.activeForm)
    return todo.activeForm
  return todo.content
}
