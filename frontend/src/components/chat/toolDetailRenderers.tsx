import type { JSX } from 'solid-js'
import { diffLines } from 'diff'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { relativizePath } from './messageUtils'
import {
  toolInputCode,
  toolInputPath,
  toolInputText,
} from './toolStyles.css'

const TRAILING_NEWLINE_RE = /\n$/

export function renderBashDetail(description?: string, _command?: string): JSX.Element | null {
  if (!description && !_command)
    return null
  const descText = description ? (description.length > 100 ? `${description.slice(0, 100)}…` : description) : ''
  return <span class={toolInputText}>{descText || 'Run command'}</span>
}

export function renderReadDetail(path?: string, offset?: number, limit?: number, cwd?: string, homeDir?: string): JSX.Element | null {
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

export function renderWriteDetail(path?: string, content?: string, cwd?: string, homeDir?: string): JSX.Element | null {
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

export function renderEditDetail(path?: string, oldStr?: string, newStr?: string, cwd?: string, homeDir?: string): JSX.Element | null {
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

export function renderSearchDetail(pattern?: string, path?: string, cwd?: string, homeDir?: string): JSX.Element | null {
  if (!pattern)
    return null
  return (
    <>
      <span class={toolInputCode}>{`"${pattern}"`}</span>
      {path ? <span class={toolInputText}>{` ${relativizePath(path, cwd, homeDir)}`}</span> : null}
    </>
  )
}

export function renderGlobDetail(pattern?: string, path?: string, cwd?: string, homeDir?: string): JSX.Element | null {
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

export function renderUrlDetail(url?: string): JSX.Element | null {
  if (!url)
    return null
  return url.startsWith('https://')
    ? <span class={toolInputText}><a href={url} target="_blank" rel="noopener noreferrer nofollow">{url}</a></span>
    : <span class={toolInputText}>{url}</span>
}

export function renderQueryDetail(query?: string): JSX.Element | null {
  return query ? <span class={toolInputText}>{query}</span> : null
}

export function renderAgentDetail(description: string, subagentType?: string): JSX.Element | null {
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
