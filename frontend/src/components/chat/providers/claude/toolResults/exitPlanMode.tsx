import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Hand from 'lucide-solid/icons/hand'
import Stamp from 'lucide-solid/icons/stamp'
import { Show } from 'solid-js'
import { pickString } from '~/lib/jsonPick'
import { relativizePath } from '~/lib/paths'
import { MarkdownText } from '../../../messageRenderers'
import { ToolStatusHeader } from '../../../results/ToolStatusHeader'
import { toolResultPrompt } from '../../../toolStyles.css'

/** ExitPlanMode result view: "Plan approved" with file path, or "Sent feedback:" with markdown content. */
export function ExitPlanModeResultView(props: {
  isError: boolean
  resultContent: string
  toolUseResult?: Record<string, unknown>
  context?: RenderContext
}): JSX.Element {
  const filePath = () => pickString(props.toolUseResult, 'filePath')
  const icon = () => props.isError ? Hand : Stamp
  const title = () => props.isError ? 'Sent feedback:' : 'Plan approved'

  return (
    <ToolStatusHeader icon={icon()} title={title()}>
      <Show when={props.isError}>
        <MarkdownText text={props.resultContent} />
      </Show>
      <Show when={!props.isError && filePath()}>
        <div class={toolResultPrompt}>
          {'Plan file: '}
          <code>{relativizePath(filePath(), props.context?.workingDir, props.context?.homeDir)}</code>
        </div>
      </Show>
    </ToolStatusHeader>
  )
}
