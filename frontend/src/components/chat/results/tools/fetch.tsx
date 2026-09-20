import type { ToolKindRenderer } from './renderer'
import Globe from 'lucide-solid/icons/globe'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from '../collapse'
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
      collapsible: hasMoreLinesThan(call.result.result, COLLAPSED_RESULT_ROWS),
      hasDiff: false,
      copyableContent: () => call.result.result || null,
    }
  },
}
