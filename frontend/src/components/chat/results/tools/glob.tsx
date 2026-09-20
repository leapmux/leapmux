import type { JSX } from 'solid-js'
import type { ToolKindRenderer } from './renderer'
import FolderSearch from 'lucide-solid/icons/folder-search'
import { relativizePath } from '~/lib/paths'
import { toolInputCode } from '../../toolStyles.css'
import { SearchResultBody, searchResultCollapsible, searchResultCopyable } from '../searchResult'
import { pathLines } from './titleParts'

/** A glob's title: the pattern, and the one path it ran in when it states any. */
export function renderGlobTitle(pattern?: string, path?: string, cwd?: string, homeDir?: string): JSX.Element | null {
  if (!pattern && !path)
    return null
  const displayPattern = pattern && pattern.startsWith('/') && !pattern.includes('*')
    ? relativizePath(pattern, cwd, homeDir)
    : (pattern || '')
  // `toolInputCode`, as `renderSearchTitle` gives its own pattern. An unclassed span
  // reaches the header as JSX, which `ToolUseLayout` leaves alone -- so the title lost
  // the monospace face and the one-line clip, and a long glob wrapped the header.
  return <span class={toolInputCode}>{`${displayPattern}${path ? ` ${relativizePath(path, cwd, homeDir)}` : ''}`}</span>
}

export const globRenderer: ToolKindRenderer<'glob'> = {
  icon: FolderSearch,
  label: 'Glob',
  title(call, context) {
    return renderGlobTitle(call.request.pattern, call.request.paths[0], context?.workingDir, context?.homeDir) ?? call.title ?? 'Glob'
  },
  summary(call, view) {
    return call.request.paths.length > 1 ? pathLines(view, call.request.paths) : null
  },
  result(call, view) {
    return <SearchResultBody source={call.result} kind="glob" {...(view.context !== undefined ? { context: view.context } : {})} />
  },
  resultMeta(call) {
    return {
      collapsible: searchResultCollapsible(call.result),
      hasDiff: false,
      copyableContent: () => searchResultCopyable(call.result) || null,
    }
  },
}
