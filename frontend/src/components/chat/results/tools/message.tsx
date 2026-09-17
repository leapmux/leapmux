import type { ToolKindRenderer } from './renderer'
import Send from 'lucide-solid/icons/send'
import { Show } from 'solid-js'
import { clipFirstLine } from '~/lib/clipFirstLine'
import { MESSAGE_PREVIEW_LIMIT } from '../../ir/tools/message'
import { toolInputSummary } from '../../toolStyles.css'
import { SendMessageRecipient } from '../SendMessageRecipient'
import { proseMeta, ProseResultBody } from './proseResult'

export const messageRenderer: ToolKindRenderer<'message'> = {
  icon: Send,
  label: 'Message',
  title(call, context) {
    return <>{call.request.to ? <SendMessageRecipient to={call.request.to} navigation={context?.subagents} /> : call.title ?? 'Message'}</>
  },
  summary(call) {
    // Clipped to one line: the summary has no height cap of its own, and a long
    // steering message would inflate the row it sits in.
    //
    // `toolInputSummary` is what every other summary hook gives its own line, and it
    // carries the monospace face, the wrap and the muted colour. This one dropped it,
    // so a preview drew in the body face at full contrast beside three that did not.
    const line = clipFirstLine(call.request.summary || call.request.text, MESSAGE_PREVIEW_LIMIT)
    return <Show when={line}><div class={toolInputSummary}>{line}</div></Show>
  },
  result(call, view) {
    return <ProseResultBody result={call.result} view={view} />
  },
  resultMeta(call) {
    return proseMeta(call.result)
  },
}
