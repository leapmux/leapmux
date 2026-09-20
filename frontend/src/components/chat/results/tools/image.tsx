import type { ToolKindMeta, ToolKindRenderer } from './renderer'
import Image from 'lucide-solid/icons/image'
import { Show } from 'solid-js'
import { toolInputSummary } from '../../toolStyles.css'

export const imageRenderer: ToolKindRenderer<'image'> = {
  icon: Image,
  label: 'Image',
  title(call) {
    return call.request.prompt ?? call.title ?? 'Image'
  },
  result(call) {
    // The pictures come from the call's own images slot, which the row draws.
    // The result says only what the generator revised the prompt into.
    return <Show when={call.result.revisedPrompt}><div class={toolInputSummary}>{call.result.revisedPrompt}</div></Show>
  },
  resultMeta(call): ToolKindMeta {
    return {
      collapsible: false,
      hasDiff: false,
      copyableContent: () => call.result.revisedPrompt ?? null,
    }
  },
}
