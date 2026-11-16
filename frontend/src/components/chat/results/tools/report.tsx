import ClipboardList from 'lucide-solid/icons/clipboard-list'
import { Show } from 'solid-js'
import { proseResult } from '../../model/toolCall'
import { proseRenderer, ProseResultBody } from './proseResult'

export const reportRenderer = proseRenderer<'report'>({
  icon: ClipboardList,
  label: 'Report',
  title(call) {
    return call.title ?? 'Report'
  },
  // A proposal that has NOT drawn its answer yet. The pending row states it while the
  // approval is open; once the result exists, the row that completed the call draws
  // the answer, as the edit family does.
  //
  // `request.proposal`, not `payload.plan`: layer 3 knows no provider and parses no
  // provider's bytes (see `results/README.md`). Reaching into the untyped payload
  // for a key one provider happens to send put a Cursor parser in the shared
  // renderer, where no other provider could reach it.
  pending(call, view) {
    return <Show when={call.request.proposal}>{text => <ProseResultBody result={proseResult(text(), 'markdown')} view={view} />}</Show>
  },
  hasPending: call => !!call.request.proposal,
  // A call that completed with no words of its own leaves the proposal as the one thing
  // to read: the row that draws the result states it. A result in words of its own
  // answers for the call instead.
  withoutTypedResult(call, view) {
    return <Show when={call.request.proposal}>{text => <ProseResultBody result={proseResult(text(), 'markdown')} view={view} />}</Show>
  },
})
