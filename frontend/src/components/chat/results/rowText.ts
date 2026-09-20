import type { ChatRow } from '../model/row'

export function quotableTextForRow(row: ChatRow | null | undefined): string | null {
  switch (row?.kind) {
    case 'assistant-text':
    case 'assistant-thinking':
    case 'assistant-plan':
    case 'plan-execution':
    case 'user':
      return row.text.trim() || null
    case 'compact-summary':
      return row.summary.trim() || null
    default:
      return null
  }
}
