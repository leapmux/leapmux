import type { JSX } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { BashInput, EditInput, GlobInput, GrepInput, ReadInput, WebFetchInput, WebSearchInput, WriteInput } from '~/types/toolMessages'
import { diffLines } from 'diff'
import { relativizePath } from './messageUtils'
import { formatTaskStatus } from './rendererUtils'
import {
  toolInputCode,
  toolInputPath,
  toolInputStatAdded,
  toolInputStatRemoved,
  toolInputText,
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
      return <span class={toolInputText}>{descText || 'Run command'}</span>
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
          <span class={toolInputText}>{rangeStr}</span>
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
          <span class={toolInputText}>{lineStr}</span>
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
            <span class={toolInputText}>
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
        ? <span class={toolInputText}><a href={url} target="_blank" rel="noopener noreferrer nofollow">{url}</a></span>
        : <span class={toolInputText}>{url}</span>
    }
    case 'WebSearch': {
      const { query } = input as WebSearchInput
      return query ? <span class={toolInputText}>{query}</span> : null
    }
    case 'TaskOutput': {
      const task = context?.childTask
      const description = task?.description
      if (description && task?.status === 'completed')
        return <span class={toolInputText}>{description}</span>
      const status = formatTaskStatus(task?.status)
      const title = `${status}${description ? ` - ${description}` : ''}`
      return <span class={toolInputText}>{title}</span>
    }
    case 'EnterPlanMode':
      return <span class={toolInputText}>Entering Plan Mode</span>
    case 'Skill': {
      const skillName = String(input.skill || '')
      return <span class={toolInputText}>{`Skill: /${skillName}`}</span>
    }
    case 'Agent':
    case 'Task': {
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
    default:
      return null
  }
}
