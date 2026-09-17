import type { ToolKindRenderer } from './renderer'
import Eye from 'lucide-solid/icons/eye'
import { relativizePath } from '~/lib/paths'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from '../../ir/collapse'
import { readFileBodyText } from '../../ir/readFileResult'
import { ReadFileResultBody } from '../readFileResult'
import { renderReadTitle } from './titleParts'

export const readRenderer: ToolKindRenderer<'read'> = {
  icon: Eye,
  label: 'Read',
  title(call, context) {
    const path = call.request.path
    // A read whose only path is the WORKING DIRECTORY has no file to show, and
    // a row titled with it reads as a bare ".".
    const readPath = path && relativizePath(path, context?.workingDir, context?.homeDir) === '.' ? '' : path
    return renderReadTitle(readPath, call.request.offset, call.request.limit, context?.workingDir, context?.homeDir) ?? call.title ?? 'Read'
  },
  result(call, view) {
    return <ReadFileResultBody source={call.result} {...(call.request.path !== undefined ? { path: call.request.path } : {})} {...(view.context !== undefined ? { context: view.context } : {})} />
  },
  resultMeta(call) {
    const lines = call.result.lines
    // `ReadFileResultBody` draws both alert lists only while the row is EXPANDED, so
    // a read whose reminders are the only thing worth showing must offer Expand.
    // Without this a partial-read notice was unreachable: the row that carried it
    // held one short body line, answered `collapsible: false`, and drew no chevron.
    const reminders = (call.result.leading?.length ?? 0) + (call.result.trailing?.length ?? 0)
    return {
      // An EMPTY list yields to the fallback, exactly as `readFileBodyText` and
      // `ReadFileResultBody` read it. `[]` is truthy, so testing the array alone
      // measured zero lines for a refused read whose reason sits in
      // `fallbackContent`, and a long reason then never offered Expand.
      collapsible: (lines && lines.length > 0
        ? lines.length > COLLAPSED_RESULT_ROWS
        : hasMoreLinesThan(call.result.fallbackContent, COLLAPSED_RESULT_ROWS))
      || reminders > 0,
      hasDiff: false,
      // Built INSIDE the closure. Hoisted, it joined every line of the file on every
      // `toolCallMeta` call -- twice per row revision, and once per streamed frame on a
      // streaming row -- to answer a boolean nobody asked to copy.
      copyableContent: () => readFileBodyText(call.result) || null,
    }
  },
}
