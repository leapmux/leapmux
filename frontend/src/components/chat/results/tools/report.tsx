import type { JSX } from 'solid-js'
import type { RequestSideCall, ToolRowView } from './renderer'
import ClipboardList from 'lucide-solid/icons/clipboard-list'
import { Show } from 'solid-js'
import { proseResult } from '../../model/toolCall'
import { textNeedsCollapse } from '../useCollapsedLines'
import { proseRenderer, ProseResultBody } from './proseResult'
import { previewWithoutResult } from './renderer'

/**
 * A proposal that has NOT drawn its answer yet.
 *
 * `request.proposal`, not `payload.plan`: layer 3 knows no provider and parses no
 * provider's bytes (see `results/README.md`). Reaching into the untyped payload for a
 * key one provider happens to send put a Cursor parser in the shared renderer, where no
 * other provider could reach it.
 */
function reportPreview(call: RequestSideCall<'report'>, view: ToolRowView): JSX.Element {
  return <Show when={call.request.proposal}>{text => <ProseResultBody result={proseResult(text(), 'markdown')} view={view} />}</Show>
}

function reportPreviewCollapsible(call: RequestSideCall<'report'>): boolean {
  return textNeedsCollapse(call.request.proposal ?? '')
}

export const reportRenderer = proseRenderer<'report'>({
  icon: ClipboardList,
  label: 'Report',
  title(call) {
    return call.title ?? 'Report'
  },
  // The pending row states the proposal while the approval is open; once the result
  // exists, the row that completed the call draws the answer, as the edit family does.
  pending: reportPreview,
  pendingCollapsible: reportPreviewCollapsible,
  // A call that ended with NO result -- the turn stopped it -- leaves the proposal as the
  // one thing to read, on the row that ended it. A result of any form answers for the
  // call instead: a declined or failed proposal states its reason, never the plan.
  withoutTypedResult: previewWithoutResult(reportPreview),
  withoutTypedResultMeta: call => ({ collapsible: call.result === undefined && reportPreviewCollapsible(call) }),
})
