import type { LucideIcon } from 'lucide-solid'
import Bot from 'lucide-solid/icons/bot'
import Eye from 'lucide-solid/icons/eye'
import FileEdit from 'lucide-solid/icons/file-pen'
import FilePlus from 'lucide-solid/icons/file-plus'
import FileSymlink from 'lucide-solid/icons/file-symlink'
import FileX from 'lucide-solid/icons/file-x'
import Folder from 'lucide-solid/icons/folder'
import FolderSearch from 'lucide-solid/icons/folder-search'
import Globe from 'lucide-solid/icons/globe'
import ListTodo from 'lucide-solid/icons/list-todo'
import Search from 'lucide-solid/icons/search'
import Terminal from 'lucide-solid/icons/terminal'
import TextSearch from 'lucide-solid/icons/text-search'
import Wrench from 'lucide-solid/icons/wrench'
import { assertNever } from '~/lib/assertNever'

/**
 * Every tool kind a `ToolPresentation` can carry.
 *
 * A CLOSED set, because four separate tables read the kind -- the icon, the
 * label, the title renderer and the input summary -- and a plain `string` let
 * them disagree. Reasonix reports `move` for a file rename, and three of the
 * four tables had no entry for it: the row drew the generic wrench and repeated
 * its input as raw JSON. Each table below is an exhaustive switch, so a new kind
 * is a compile error in every table that must learn it.
 *
 * The empty string is the state "the provider states no kind". It is a real
 * value on the wire, and `toolKindLabel` answers `Tool` for it.
 */
export const TOOL_KINDS = [
  '',
  'agent',
  'delete',
  'edit',
  'execute',
  'fetch',
  'glob',
  'grep',
  'list',
  'move',
  'other',
  'read',
  'search',
  'think',
  'todo',
  'write',
] as const

export type ToolKind = (typeof TOOL_KINDS)[number]

const KNOWN_TOOL_KINDS: ReadonlySet<string> = new Set(TOOL_KINDS)

/**
 * Narrows a wire value to one tool kind.
 *
 * A kind LeapMux does not know becomes `other`, which is the catch-all the
 * Agent Client Protocol itself gives for a tool that fits no category. The
 * empty string keeps its own meaning, because a provider that states no kind
 * said something different from a provider that called the tool uncategorized.
 */
export function toolKind(value: string | undefined): ToolKind {
  if (value === undefined)
    return ''
  return KNOWN_TOOL_KINDS.has(value) ? value as ToolKind : 'other'
}

/** Icon for a tool kind. */
export function toolKindIcon(kind: ToolKind): LucideIcon {
  switch (kind) {
    case 'agent': return Bot
    case 'todo': return ListTodo
    case 'write': return FilePlus
    case 'delete': return FileX
    case 'fetch': return Globe
    case 'glob': return FolderSearch
    case 'list': return Folder
    case 'grep': return TextSearch
    case 'execute': return Terminal
    case 'edit': return FileEdit
    case 'move': return FileSymlink
    case 'read': return Eye
    case 'search': return Search
    case '':
    case 'other':
    case 'think':
      return Wrench
    default: {
      assertNever(kind)
    }
  }
}

/** The tool name a row shows when the provider supplies no label of its own. */
export function toolKindLabel(kind: ToolKind): string {
  switch (kind) {
    case 'agent': return 'Agent'
    case 'delete': return 'Delete'
    case 'edit': return 'Edit'
    case 'execute': return 'Execute'
    case 'fetch': return 'Fetch'
    case 'glob': return 'Glob'
    case 'grep': return 'Grep'
    case 'list': return 'List'
    case 'move': return 'Move'
    case 'other': return 'Other'
    case 'read': return 'Read'
    case 'search': return 'Search'
    case 'think': return 'Think'
    case 'todo': return 'Todo'
    case 'write': return 'Write'
    case '': return 'Tool'
    default: {
      assertNever(kind)
    }
  }
}
