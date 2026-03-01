import type { JSX } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { BashInput, EditInput, GlobInput, GrepInput, ReadInput, WebFetchInput, WebSearchInput, WriteInput } from '~/types/toolMessages'
import { diffLines } from 'diff'
import { relativizePath } from './messageUtils'
import {
  toolInputCode,
  toolInputDetail,
  toolInputPath,
  toolInputStatAdded,
  toolInputStatRemoved,
  toolInputSubDetail,
  toolInputSubDetailExpanded,
} from './toolStyles.css'

/** Render per-tool compact display for a tool_use block. */
export function renderToolDetail(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const cwd = context?.workingDir
  const homeDir = context?.homeDir

  switch (toolName) {
    case 'Bash': {
      const { description: desc, command: cmd } = input as BashInput
      if (!desc && !cmd)
        return null
      const descText = desc ? (desc.length > 100 ? `${desc.slice(0, 100)}…` : desc) : ''
      return <span class={toolInputDetail}>{descText || 'Run command'}</span>
    }
    case 'Read': {
      const { file_path: path, offset, limit } = input as ReadInput
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
          <span class={toolInputDetail}>{rangeStr}</span>
        </>
      )
    }
    case 'Write': {
      const { file_path: path, content } = input as WriteInput
      if (!path)
        return null
      const lineCount = content ? content.split('\n').length : 0
      const lineStr = lineCount > 0 ? ` (${lineCount} ${lineCount === 1 ? 'line' : 'lines'})` : ''
      return (
        <>
          <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
          <span class={toolInputDetail}>{lineStr}</span>
        </>
      )
    }
    case 'Edit': {
      const { file_path: path, old_string: oldStr, new_string: newStr } = input as EditInput
      if (!path)
        return null
      let added = 0
      let removed = 0
      if (oldStr && newStr && oldStr !== newStr) {
        const changes = diffLines(oldStr, newStr)
        for (const c of changes) {
          const count = c.value.replace(/\n$/, '').split('\n').length
          if (c.added)
            added += count
          else if (c.removed)
            removed += count
        }
      }
      const hasStats = added > 0 || removed > 0
      return (
        <>
          <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
          {hasStats && (
            <span class={toolInputDetail}>
              {' '}
              <span class={toolInputStatAdded}>
                {`+${added}`}
              </span>
              {' '}
              <span class={toolInputStatRemoved}>
                {`-${removed}`}
              </span>
            </span>
          )}
        </>
      )
    }
    case 'Grep': {
      const { pattern } = input as GrepInput
      return pattern
        ? <span class={toolInputCode}>{`"${pattern}"`}</span>
        : null
    }
    case 'Glob': {
      const { pattern, path } = input as GlobInput
      // Relativize pattern if it's an absolute path without glob wildcards
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
    case 'WebFetch': {
      const { url } = input as WebFetchInput
      if (!url)
        return null
      return url.startsWith('https://')
        ? <span class={toolInputDetail}><a href={url} target="_blank" rel="noopener noreferrer nofollow">{url}</a></span>
        : <span class={toolInputDetail}>{url}</span>
    }
    case 'WebSearch': {
      const { query } = input as WebSearchInput
      return query ? <span class={toolInputDetail}>{query}</span> : null
    }
    default:
      return null
  }
}

/** Render an optional sub-detail line below the tool header (e.g. Bash command, Grep path). */
export function renderToolSubDetail(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const expanded = context?.threadExpanded
  const cls = expanded ? toolInputSubDetailExpanded : toolInputSubDetail
  switch (toolName) {
    case 'Bash': {
      const { command: cmd } = input as BashInput
      if (!cmd)
        return null
      if (expanded) {
        return <div class={cls}>{cmd}</div>
      }
      const firstLine = cmd.split('\n')[0]
      const truncated = firstLine.length > 120 ? `${firstLine.slice(0, 120)}…` : firstLine
      return <div class={cls}>{truncated}</div>
    }
    case 'Grep': {
      const { path } = input as GrepInput
      if (!path)
        return null
      return <div class={cls}>{relativizePath(path, context?.workingDir, context?.homeDir)}</div>
    }
    default:
      return null
  }
}
