import type { ToolKindRenderer } from './renderer'
import Globe from 'lucide-solid/icons/globe'
import { textNeedsCollapse } from '../useCollapsedLines'
import { WebFetchResultBody } from '../webFetchResult'
import { renderUrlTitle } from './titleParts'

export const fetchRenderer: ToolKindRenderer<'fetch'> = {
  icon: Globe,
  label: 'Fetch',
  title(call) {
    return renderUrlTitle(call.request.url) ?? call.title ?? 'Fetch'
  },
  result(call, view) {
    return <WebFetchResultBody source={call.result} {...(view.context !== undefined ? { context: view.context } : {})} />
  },
  resultMeta(call) {
    return {
      collapsible: textNeedsCollapse(call.result.result),
      hasDiff: false,
      copyableContent: () => call.result.result || null,
    }
  },
}
