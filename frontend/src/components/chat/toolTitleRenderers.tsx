import type { JSX } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { ToolKind } from './results/toolKind'
import type { ToolPresentation } from './results/toolPresentation'
import { diffLines } from 'diff'
import { Show } from 'solid-js'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { isObject, pickFirstString, pickNumber, pickString } from '~/lib/jsonPick'
import { relativizePath } from '~/lib/paths'
import { pluralize } from '~/lib/plural'
import { UNTRUSTED_LINK_ATTRIBUTE } from '~/lib/untrustedLinkClicks'
import { FileEditDiffTitle } from './results/fileEditDiff'
import { TOOL_FILE_PATH_KEYS, TOOL_NEW_TEXT_KEYS, TOOL_OLD_TEXT_KEYS } from './results/toolInputs'
import {
  toolInputCode,
  toolInputPath,
  toolInputText,
} from './toolStyles.css'

const TRAILING_NEWLINE_RE = /\n$/

const INPUT_HINT_KEYS = ['query', 'input', 'prompt', 'text', 'command', 'description', 'url']

function shortInputHint(value: unknown): string {
  return typeof value === 'string' && value.length > 0 && value.length <= 120
    ? value.length > 80 ? `${value.slice(0, 80)}…` : value
    : ''
}

/** Prefer a common argument key, then the first short string. */
export function toolInputHint(input: unknown): string {
  if (!isObject(input))
    return ''
  for (const key of INPUT_HINT_KEYS) {
    const hint = shortInputHint(input[key])
    if (hint)
      return hint
  }
  for (const value of Object.values(input)) {
    const hint = shortInputHint(value)
    if (hint)
      return hint
  }
  return ''
}

export function renderMcpTitle(displayName: string, input: unknown): JSX.Element {
  const hint = toolInputHint(input)
  return (
    <>
      <span class={toolInputText}>{displayName}</span>
      <Show when={hint}><span class={toolInputCode}>{` "${hint}"`}</span></Show>
    </>
  )
}

export function renderBashTitle(description?: string, command?: string): JSX.Element | null {
  if (!description && !command)
    return null
  const descText = description ? (description.length > 100 ? `${description.slice(0, 100)}…` : description) : ''
  return <span class={toolInputText}>{descText || 'Run command'}</span>
}

export function renderReadTitle(path?: string, offset?: number, limit?: number, cwd?: string, homeDir?: string): JSX.Element | null {
  if (!path)
    return null
  const start = offset !== undefined && Number.isSafeInteger(offset) && offset > 0 ? offset : undefined
  const count = limit !== undefined && Number.isSafeInteger(limit) && limit > 0 ? limit : undefined
  const end = count !== undefined && count - 1 <= Number.MAX_SAFE_INTEGER - (start ?? 1) ? (start ?? 1) + (count - 1) : undefined
  const rangeStr = end !== undefined
    ? ` (Line ${start ?? 1}–${end})`
    : start !== undefined ? ` (Line ${start}–)` : ''
  return (
    <>
      <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
      <span class={toolInputText}>{rangeStr}</span>
    </>
  )
}

export function renderWriteTitle(path?: string, content?: string, cwd?: string, homeDir?: string): JSX.Element | null {
  if (!path)
    return null
  const lineCount = content ? content.replace(TRAILING_NEWLINE_RE, '').split('\n').length : 0
  const lineStr = lineCount > 0 ? ` (${pluralize(lineCount, 'line')})` : ''
  return (
    <>
      <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
      <span class={toolInputText}>{lineStr}</span>
    </>
  )
}

export function renderDeleteTitle(path?: string, cwd?: string, homeDir?: string): JSX.Element | null {
  if (!path)
    return null
  return <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
}

export function renderEditTitle(path?: string, oldStr?: string, newStr?: string, replaceAll?: boolean, cwd?: string, homeDir?: string): JSX.Element | null {
  if (!path)
    return null
  let added = 0
  let removed = 0
  if (oldStr !== undefined && newStr !== undefined && oldStr !== newStr) {
    const changes = diffLines(oldStr, newStr)
    for (const c of changes) {
      const count = c.value.replace(TRAILING_NEWLINE_RE, '').split('\n').length
      if (c.added)
        added += count
      else if (c.removed)
        removed += count
    }
  }
  return (
    <>
      <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
      <DiffStatsBadge stats={{ added, deleted: removed, untracked: 0 }} class={toolInputText} />
      <Show when={replaceAll}>
        <span class={toolInputText}>{' (replace all)'}</span>
      </Show>
    </>
  )
}

export function renderSearchTitle(pattern?: string, path?: string, cwd?: string, homeDir?: string): JSX.Element | null {
  if (!pattern)
    return null
  return (
    <>
      <span class={toolInputCode}>{`"${pattern}"`}</span>
      {path ? <span class={toolInputText}>{` ${relativizePath(path, cwd, homeDir)}`}</span> : null}
    </>
  )
}

export function renderGlobTitle(pattern?: string, path?: string, cwd?: string, homeDir?: string): JSX.Element | null {
  if (!pattern && !path)
    return null
  const displayPattern = pattern && pattern.startsWith('/') && !pattern.includes('*')
    ? relativizePath(pattern, cwd, homeDir)
    : (pattern || '')
  return (
    <span class={toolInputCode}>
      {displayPattern}
      {path ? ` ${relativizePath(path, cwd, homeDir)}` : ''}
    </span>
  )
}

export function renderUrlTitle(url?: string): JSX.Element | null {
  if (!url)
    return null
  return url.startsWith('https://')
    ? (
        <span class={toolInputText}>
          {/* Agent-authored, so the click takes the same prompt a terminal
              hyperlink takes -- see `interceptUntrustedLinkClicks`. */}
          <a href={url} target="_blank" rel="noopener noreferrer nofollow" {...{ [UNTRUSTED_LINK_ATTRIBUTE]: '' }}>{url}</a>
        </span>
      )
    : <span class={toolInputText}>{url}</span>
}

export function renderQueryTitle(query?: string): JSX.Element | null {
  return query ? <span class={toolInputText}>{query}</span> : null
}

export function renderAgentTitle(description: string, subagentType?: string): JSX.Element | null {
  // If description starts with subagent name, use "SubAgent: rest" format;
  // also suppress the trailing "(SubAgent)" suffix since it's already in the title.
  let titleDesc = description
  let showSuffix = true
  if (subagentType) {
    const prefix = subagentType.toLowerCase()
    const descLower = description.toLowerCase()
    if (descLower.startsWith(`${prefix} `)) {
      titleDesc = `${subagentType}: ${description.slice(subagentType.length + 1)}`
      showSuffix = false
    }
  }

  const title = `${titleDesc}${showSuffix && subagentType ? ` (${subagentType})` : ''}`
  return <span class={toolInputText}>{title}</span>
}

/**
 * The kinds {@link toolMessageTitle} writes a title for.
 *
 * `ToolMessage` repeats a tool's input as JSON only for the kinds OUTSIDE this
 * set, so the row never states the same input twice. A kind here whose input
 * carries no path or pattern the renderer recognizes keeps the tool's own
 * title, and that is the same answer `edit`, `read` and `list` already give.
 */
export const TITLED_TOOL_KINDS: ReadonlySet<ToolKind> = new Set<ToolKind>([
  'agent',
  'delete',
  'edit',
  'execute',
  'fetch',
  'glob',
  'grep',
  'list',
  'move',
  'read',
  'search',
  'write',
])

/**
 * The header title of one tool row.
 *
 * It lives here, beside the renderers it dispatches to, rather than inside
 * `ToolMessage`: it reads no signal of that component and answers from the
 * presentation model alone. The switch is exhaustive over {@link ToolKind}, so
 * a new kind cannot reach the reader as a bare tool name by accident.
 */
export function toolMessageTitle(model: ToolPresentation, context?: RenderContext): JSX.Element {
  const args = model.input
  const path = pickFirstString(args, TOOL_FILE_PATH_KEYS)
  // A read whose only path is the WORKING DIRECTORY has no file to name, and a
  // row titled with it reads as a bare ".". Cursor's `ReadLints` reaches here:
  // it declares ACP kind `read`, sends `title: "Read Lints"`, and gives the
  // working directory as its only location -- so the title it sent lost to a
  // path that says nothing. `list` keeps that path on purpose, because listing
  // the working directory IS what "." means there.
  const readPath = () => path && relativizePath(path, context?.workingDir, context?.homeDir) === '.' ? '' : path
  if (model.kind === 'write' && typeof args.content === 'string')
    return renderWriteTitle(path, args.content, context?.workingDir, context?.homeDir) || model.title
  const changes = model.body.type === 'diff' ? model.body.sources : model.requestedChanges
  if (changes?.length) {
    if (changes.length === 1)
      return <FileEditDiffTitle source={model.body.type === 'diff' ? changes[0] : { ...changes[0], operation: undefined }} context={context} />
    const paths = new Set(changes.map(source => source.filePath))
    return paths.size === 1
      ? `${changes.length} changes in ${relativizePath(changes[0].filePath, context?.workingDir, context?.homeDir)}`
      : `${paths.size} files${model.body.type === 'diff' ? ' changed' : ''}`
  }
  switch (model.kind) {
    case 'agent': return renderAgentTitle(model.title, model.agentRequest?.agentType)
    case 'execute': return renderBashTitle(pickString(args, 'description') || (model.title !== pickString(args, 'command') && model.title !== model.kind ? model.title : ''), pickString(args, 'command')) || model.title || 'Run command'
    case 'read': return renderReadTitle(readPath(), pickNumber(args, 'offset', undefined), pickNumber(args, 'limit', undefined), context?.workingDir, context?.homeDir) || model.title
    case 'list': return renderReadTitle(path || '.', undefined, undefined, context?.workingDir, context?.homeDir) || model.title
    case 'glob': return renderGlobTitle(pickString(args, 'pattern'), path, context?.workingDir, context?.homeDir) || model.title
    case 'grep': return renderSearchTitle(pickString(args, 'pattern'), undefined, context?.workingDir, context?.homeDir) || model.title
    case 'search': return renderSearchTitle(pickString(args, 'pattern') || pickString(args, 'query'), path, context?.workingDir, context?.homeDir) || model.title
    // A move states its destination the same way as an edit, and
    // `FileEditDiffTitle` above already drew the rename arrow whenever the
    // provider resolved both paths.
    case 'edit':
    case 'write':
    case 'delete':
    case 'move': return renderEditTitle(path, pickFirstString(args, TOOL_OLD_TEXT_KEYS), pickFirstString(args, TOOL_NEW_TEXT_KEYS), undefined, context?.workingDir, context?.homeDir) || model.title
    case 'fetch': return renderUrlTitle(pickString(args, 'url')) || model.title
    case '':
    case 'other':
    case 'think':
    case 'todo':
      return model.title
    default: {
      const exhaustive: never = model.kind
      void exhaustive
      return model.title
    }
  }
}
