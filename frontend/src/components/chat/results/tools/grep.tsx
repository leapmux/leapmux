import type { ToolKindRenderer } from './renderer'
import TextSearch from 'lucide-solid/icons/text-search'
import { searchResultCollapsible, searchResultCopyable } from '../../ir/searchResult'
import { SearchResultBody } from '../searchResult'
import { pathLines, renderSearchTitle } from './titleParts'

export const grepRenderer: ToolKindRenderer<'grep'> = {
  icon: TextSearch,
  label: 'Grep',
  title(call, context) {
    return renderSearchTitle(call.request.pattern, undefined, context?.workingDir, context?.homeDir) ?? call.title ?? 'Grep'
  },
  summary(call, view) {
    return pathLines(view, call.request.paths)
  },
  result(call, view) {
    return <SearchResultBody source={call.result} kind="grep" {...(view.context !== undefined ? { context: view.context } : {})} />
  },
  resultMeta(call) {
    return {
      collapsible: searchResultCollapsible(call.result),
      hasDiff: false,
      copyableContent: () => searchResultCopyable(call.result) || null,
    }
  },
}
