import type { ToolKindRenderer } from './renderer'
import Folder from 'lucide-solid/icons/folder'
import { ListResultBody, listResultCollapsible } from '../listResult'
import { renderReadTitle } from './titleParts'

export const listRenderer: ToolKindRenderer<'list'> = {
  icon: Folder,
  label: 'List',
  title(call, context) {
    return renderReadTitle(call.request.path || '.', undefined, undefined, context?.workingDir, context?.homeDir) ?? call.title ?? 'List'
  },
  result(call, view) {
    return <ListResultBody source={call.result} {...(view.context !== undefined ? { context: view.context } : {})} />
  },
  resultMeta(call) {
    return {
      collapsible: listResultCollapsible(call.result),
      hasDiff: false,
      copyableContent: () => call.result.entries.map(entry => entry.path).join('\n') || null,
    }
  },
}
