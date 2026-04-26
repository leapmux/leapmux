import type { LucideIcon } from 'lucide-solid'
import File from 'lucide-solid/icons/file'
import FileEdit from 'lucide-solid/icons/file-pen-line'
import Search from 'lucide-solid/icons/search'
import Terminal from 'lucide-solid/icons/terminal'
import Wrench from 'lucide-solid/icons/wrench'
import { isObject } from '~/lib/jsonPick'

/** Icon for a tool kind. */
export function kindIcon(kind: string | undefined): LucideIcon {
  switch (kind) {
    case 'execute': return Terminal
    case 'edit': return FileEdit
    case 'read': return File
    case 'search': return Search
    default: return Wrench
  }
}

/** Capitalize a tool kind for display as a tool name. */
export function kindLabel(kind: string | undefined): string {
  if (!kind)
    return 'Tool'
  return kind.charAt(0).toUpperCase() + kind.slice(1)
}

/** Extract text content from agent_message_chunk. */
export function extractAgentText(parsed: unknown): string {
  if (!isObject(parsed))
    return ''
  const content = (parsed as Record<string, unknown>).content
  if (isObject(content))
    return String((content as Record<string, unknown>).text || '')
  return ''
}
