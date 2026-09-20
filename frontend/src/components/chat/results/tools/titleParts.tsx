import type { JSX } from 'solid-js'
import type { ToolRowView } from './renderer'
import { For } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { UNTRUSTED_LINK_ATTRIBUTE } from '~/lib/untrustedLinkClicks'
import { toolInputCode, toolInputPath, toolInputSummary, toolInputText } from '../../toolStyles.css'

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

export function renderUrlTitle(url?: string): JSX.Element | null {
  if (!url)
    return null
  return url.startsWith('https://')
    ? (
        <span class={toolInputText}>
          {/* The agent wrote this, so the click takes the same prompt a terminal
              hyperlink takes-- see `interceptUntrustedLinkClicks`. */}
          <a href={url} target="_blank" rel="noopener noreferrer nofollow" {...{ [UNTRUSTED_LINK_ATTRIBUTE]: '' }}>{url}</a>
        </span>
      )
    : <span class={toolInputText}>{url}</span>
}

export function renderQueryTitle(query?: string): JSX.Element | null {
  return query ? <span class={toolInputText}>{query}</span> : null
}

/**
 * One line per path a search restricted itself to.
 *
 * Shared, because `glob` and `grep` drew the identical `<For>` over the same
 * relativized paths and only one of them carried the guard.
 */
export function pathLines(view: ToolRowView, paths: string[]): JSX.Element {
  return <><For each={paths}>{path => <div class={toolInputSummary}>{relativizePath(path, view.context?.workingDir, view.context?.homeDir)}</div>}</For></>
}
