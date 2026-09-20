import type { TodoItem } from '~/models/todo'

export interface TodoRequest { items: TodoItem[], note?: string }
export interface TodoResult { items: TodoItem[], emptyText?: string, note?: string }
