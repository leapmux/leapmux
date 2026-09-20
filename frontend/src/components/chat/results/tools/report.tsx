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
  request(call, view) {
    // A proposal that has NOT drawn its answer yet. The proposing row states it while
    // the approval is open; once the result exists, the same rule the edit family
    // follows hands the drawing to the row that completed the call.
    //
    // `request.proposal`, not `payload.plan`: layer 3 knows no provider and parses no
    // provider's bytes (see `results/README.md`). Reaching into the untyped payload
    // for a key one provider happens to send put a Cursor parser in the shared
    // renderer, where no other provider could reach it.
    const proposal = view.drawsResult && call.result === undefined ? call.request.proposal : undefined
    return <Show when={proposal}>{text => <ProseResultBody result={proseResult(text(), 'markdown')} view={view} />}</Show>
  },
})
