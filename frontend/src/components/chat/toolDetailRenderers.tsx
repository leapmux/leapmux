import type { JSX } from 'solid-js'
import type { BashInput, EditInput, GlobInput, GrepInput, ReadInput, WebFetchInput, WebSearchInput, WriteInput } from '~/types/toolMessages'
import { diffLines } from 'diff'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { relativizePath } from './messageUtils'
import {
  toolInputCode,
  toolInputPath,
  toolInputText,
} from './toolStyles.css'

const TRAILING_NEWLINE_RE = /\n$/

export function renderBashDetail(input: BashInput): JSX.Element | null {
  const { description: desc, command: cmd } = input
  if (!desc && !cmd)
    return null
  const descText = desc ? (desc.length > 100 ? `${desc.slice(0, 100)}…` : desc) : ''
  return <span class={toolInputText}>{descText || 'Run command'}</span>
}

export function renderReadDetail(input: ReadInput, cwd?: string, homeDir?: string): JSX.Element | null {
  const { file_path: path, offset, limit } = input
  if (!path)
    return null
  const rangeStr = offset && limit
    ? ` (Line ${offset}–${offset + limit - 1})`
    : limit
      ? ` (Line 1–${limit})`
      : offset
        ? ` (Line ${offset}–)`
        : ''
  return (
    <>
      <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
      <span class={toolInputText}>{rangeStr}</span>
    </>
  )
}

export function renderWriteDetail(input: WriteInput, cwd?: string, homeDir?: string): JSX.Element | null {
  const { file_path: path, content } = input
  if (!path)
    return null
  const lineCount = content ? content.split('\n').length : 0
  const lineStr = lineCount > 0 ? ` (${lineCount} ${lineCount === 1 ? 'line' : 'lines'})` : ''
  return (
    <>
      <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
      <span class={toolInputText}>{lineStr}</span>
    </>
  )
}

export function renderEditDetail(input: EditInput, cwd?: string, homeDir?: string): JSX.Element | null {
  const { file_path: path, old_string: oldStr, new_string: newStr } = input
  if (!path)
    return null
  let added = 0
  let removed = 0
  if (oldStr && newStr && oldStr !== newStr) {
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
      <DiffStatsBadge added={added} deleted={removed} class={toolInputText} />
    </>
  )
}

export function renderGrepDetail(input: GrepInput): JSX.Element | null {
  const { pattern } = input
  return pattern
    ? <span class={toolInputCode}>{`"${pattern}"`}</span>
    : null
}

export function renderGlobDetail(input: GlobInput, cwd?: string, homeDir?: string): JSX.Element | null {
  const { pattern, path } = input
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

export function renderWebFetchDetail(input: WebFetchInput): JSX.Element | null {
  const { url } = input
  if (!url)
    return null
  return url.startsWith('https://')
    ? <span class={toolInputText}><a href={url} target="_blank" rel="noopener noreferrer nofollow">{url}</a></span>
    : <span class={toolInputText}>{url}</span>
}

export function renderWebSearchDetail(input: WebSearchInput): JSX.Element | null {
  const { query } = input
  return query ? <span class={toolInputText}>{query}</span> : null
}

export function renderAgentDetail(input: Record<string, unknown>, toolName: string): JSX.Element | null {
  const description = String(input.description || toolName)
  const subagentType = input.subagent_type ? String(input.subagent_type) : null

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
