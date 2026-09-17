import type { ToolKindRenderer } from './renderer'
import Search from 'lucide-solid/icons/search'
import { searchResultCollapsible, searchResultCopyable } from '../../ir/searchResult'
import { SearchResultBody } from '../searchResult'
import { renderSearchTitle } from './titleParts'

export const searchRenderer: ToolKindRenderer<'search'> = {
  icon: Search,
  label: 'Search',
  title(call, context) {
    return renderSearchTitle(call.request.pattern, call.request.paths[0], context?.workingDir, context?.homeDir) ?? call.title ?? 'Search'
  },
  result(call, view) {
    return <SearchResultBody source={call.result} kind="search" {...(view.context !== undefined ? { context: view.context } : {})} />
  },
  resultMeta(call) {
    return {
      collapsible: searchResultCollapsible(call.result),
      hasDiff: false,
      copyableContent: () => searchResultCopyable(call.result) || null,
    }
  },
}
