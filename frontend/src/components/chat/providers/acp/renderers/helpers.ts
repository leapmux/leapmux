import type { LucideIcon } from 'lucide-solid'
import Bot from 'lucide-solid/icons/bot'
import Eye from 'lucide-solid/icons/eye'
import FileEdit from 'lucide-solid/icons/file-pen'
import FilePlus from 'lucide-solid/icons/file-plus'
import FileX from 'lucide-solid/icons/file-x'
import Folder from 'lucide-solid/icons/folder'
import FolderSearch from 'lucide-solid/icons/folder-search'
import Globe from 'lucide-solid/icons/globe'
import ListTodo from 'lucide-solid/icons/list-todo'
import Search from 'lucide-solid/icons/search'
import Terminal from 'lucide-solid/icons/terminal'
import TextSearch from 'lucide-solid/icons/text-search'
import Wrench from 'lucide-solid/icons/wrench'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { ACP_TOOL_KIND } from '~/types/toolMessages'
import { capitalize } from '../../../rendererUtils'

/** Icon for a tool kind. */
export function kindIcon(kind: string | undefined): LucideIcon {
  switch (kind) {
    case 'agent': return Bot
    case 'todo': return ListTodo
    case 'write': return FilePlus
    case 'delete': return FileX
    case 'fetch': return Globe
    case 'glob': return FolderSearch
    case 'list': return Folder
    case 'grep': return TextSearch
    case ACP_TOOL_KIND.EXECUTE: return Terminal
    case ACP_TOOL_KIND.EDIT: return FileEdit
    case ACP_TOOL_KIND.READ: return Eye
    case ACP_TOOL_KIND.SEARCH: return Search
    default: return Wrench
  }
}

/** Capitalize a tool kind for display as a tool name. */
export function kindLabel(kind: string | undefined): string {
  if (!kind)
    return 'Tool'
  return capitalize(kind)
}

/** Extract text content from agent_message_chunk. */
export function extractAgentText(parsed: unknown): string {
  if (!isObject(parsed))
    return ''
  return pickString(pickObject(parsed, 'content'), 'text')
}
