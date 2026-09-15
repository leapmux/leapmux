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
import { TOOL_DESTINATION_PATH_KEYS, TOOL_FILE_PATH_KEYS, TOOL_NEW_TEXT_KEYS, TOOL_OLD_TEXT_KEYS, TOOL_SOURCE_PATH_KEYS } from './results/toolInputs'
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

/**
 * A move's title: the source, an arrow, and the destination.
 *
 * A move states TWO paths and neither is a `filePath`, so `renderEditTitle`
 * (which asks for one) returned null for every unfinished move and left the row
 * showing the bare tool name. A COMPLETED move does not reach here, because the
 * provider rewrites it to a `diff` body and `FileEditDiffTitle` draws the rename
 * arrow. This serves the running row, the failed one and the cancelled one.
 */
export function renderMoveTitle(source?: string, destination?: string, cwd?: string, homeDir?: string): JSX.Element | null {
  if (!source && !destination)
    return null
  const show = (path: string) => relativizePath(path, cwd, homeDir)
  if (!source || !destination)
    return <span class={toolInputPath}>{show(source || destination!)}</span>
  return (
    <>
      <span class={toolInputPath}>{show(source)}</span>
      <span class={toolInputText}>{' → '}</span>
      <span class={toolInputPath}>{show(destination)}</span>
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

function titlePath(model: ToolPresentation): string | undefined {
  return pickFirstString(model.input, TOOL_FILE_PATH_KEYS)
}

function renderEditKindTitle(model: ToolPresentation, context?: RenderContext): JSX.Element {
  return renderEditTitle(
    titlePath(model),
    pickFirstString(model.input, TOOL_OLD_TEXT_KEYS),
    pickFirstString(model.input, TOOL_NEW_TEXT_KEYS),
    model.replaceAll,
    context?.workingDir,
    context?.homeDir,
  ) || model.title
}

/**
 * The title renderer of each tool kind, or null for a kind that keeps the tool's
 * own title.
 *
 * ONE table, not a table beside a set. `TITLED_TOOL_KINDS` stated the same fact
 * a second time, as a `ReadonlySet<ToolKind>` that TypeScript never checks for
 * coverage, and a probe test existed only to hold the two in step. A
 * `Record<ToolKind, …>` is coverage-checked by the compiler, which is what the
 * doc on {@link ToolKind} already claims of every table beside it.
 *
 * Each entry returns the FINAL title, including its own fallback to
 * `model.title`, so {@link toolMessageTitle} holds one dispatch and no second
 * layer of `||`.
 */
const TOOL_TITLE_RENDERERS: Record<ToolKind, ((model: ToolPresentation, context?: RenderContext) => JSX.Element) | null> = {
  'agent': model => renderAgentTitle(model.title, model.agentRequest?.agentType),
  'execute': model => renderBashTitle(
    pickString(model.input, 'description') || (model.title !== pickString(model.input, 'command') && model.title !== model.kind ? model.title : ''),
    pickString(model.input, 'command'),
  ) || model.title || 'Run command',
  // A read whose only path is the WORKING DIRECTORY has no file to name, and a
  // row titled with it reads as a bare ".". Cursor's `ReadLints` reaches here:
  // it declares ACP kind `read`, sends `title: "Read Lints"`, and gives the
  // working directory as its only location -- so the title it sent lost to a
  // path that says nothing. `list` keeps that path on purpose, because listing
  // the working directory IS what "." means there.
  'read': (model, context) => {
    const path = titlePath(model)
    const readPath = path && relativizePath(path, context?.workingDir, context?.homeDir) === '.' ? '' : path
    return renderReadTitle(readPath, pickNumber(model.input, 'offset', undefined), pickNumber(model.input, 'limit', undefined), context?.workingDir, context?.homeDir) || model.title
  },
  'list': (model, context) => renderReadTitle(titlePath(model) || '.', undefined, undefined, context?.workingDir, context?.homeDir) || model.title,
  'glob': (model, context) => renderGlobTitle(pickString(model.input, 'pattern'), titlePath(model), context?.workingDir, context?.homeDir) || model.title,
  // `pattern || query`, like 'search' below and like acpToolNeedsResult, which
  // accepts either key for a grep kind. Reading `pattern` alone left a provider that
  // spells it `query` with no title AND no raw-input summary, because
  // kindHasTitleRenderer suppresses that summary from the KIND rather than from
  // whether a title was produced -- so the row stated neither the pattern nor the path.
  'grep': (model, context) => renderSearchTitle(pickString(model.input, 'pattern') || pickString(model.input, 'query'), undefined, context?.workingDir, context?.homeDir) || model.title,
  'search': (model, context) => renderSearchTitle(pickString(model.input, 'pattern') || pickString(model.input, 'query'), titlePath(model), context?.workingDir, context?.homeDir) || model.title,
  'edit': renderEditKindTitle,
  'write': renderEditKindTitle,
  'delete': renderEditKindTitle,
  // A move states its source and its destination, and neither arrives under a
  // `filePath` key. `FileEditDiffTitle` in {@link toolMessageTitle} draws the
  // rename arrow once the provider resolves both paths into a diff, so this
  // entry serves the row that it has not: the running move, the failed one and
  // the cancelled one.
  'move': (model, context) => renderMoveTitle(
    pickFirstString(model.input, TOOL_SOURCE_PATH_KEYS) || titlePath(model),
    pickFirstString(model.input, TOOL_DESTINATION_PATH_KEYS),
    context?.workingDir,
    context?.homeDir,
  ) || model.title,
  'fetch': model => renderUrlTitle(pickString(model.input, 'url')) || model.title,
  '': null,
  'other': null,
  'think': null,
  'todo': null,
}

/**
 * Whether {@link toolMessageTitle} writes a title for this kind.
 *
 * `ToolMessage` repeats a tool's input as JSON only for a kind that answers
 * false, so the row never states the same input twice. A kind that answers true
 * and whose input carries no path or pattern its renderer reads keeps the tool's
 * own title, and that is the same answer `edit`, `read` and `list` already give.
 */
export function kindHasTitleRenderer(kind: ToolKind): boolean {
  return TOOL_TITLE_RENDERERS[kind] !== null
}

/**
 * The header title of one tool row.
 *
 * It lives here, beside the renderers it dispatches to, rather than inside
 * `ToolMessage`: it reads no signal of that component and answers from the
 * presentation model alone. {@link TOOL_TITLE_RENDERERS} is total over
 * {@link ToolKind}, so a new kind cannot reach the reader as a bare tool name by
 * accident.
 */
export function toolMessageTitle(model: ToolPresentation, context?: RenderContext): JSX.Element {
  const args = model.input
  // BEFORE the table, and before the `changes` block, on purpose. A write row can
  // carry `input.content` AND a one-file `requestedChanges` at the same time --
  // OpenCode and Kilo set the kind FROM that content, while acpToolBase has already
  // built the diff from `{filePath, content}` -- and this precedence is what keeps
  // its "(N lines)" title instead of the rename-arrow diff title. It is NOT a
  // duplicate of the table's `write` entry, so folding it in changes what those rows
  // read.
  if (model.kind === 'write' && typeof args.content === 'string')
    return renderWriteTitle(titlePath(model), args.content, context?.workingDir, context?.homeDir) || model.title
  const changes = model.body.type === 'diff' ? model.body.sources : model.requestedChanges
  if (changes?.length) {
    if (changes.length === 1)
      return <FileEditDiffTitle source={model.body.type === 'diff' ? changes[0] : { ...changes[0], operation: undefined }} context={context} />
    const paths = new Set(changes.map(source => source.filePath))
    return paths.size === 1
      ? `${changes.length} changes in ${relativizePath(changes[0].filePath, context?.workingDir, context?.homeDir)}`
      : `${paths.size} files${model.body.type === 'diff' ? ' changed' : ''}`
  }
  const render = TOOL_TITLE_RENDERERS[model.kind]
  return render ? render(model, context) : model.title
}
