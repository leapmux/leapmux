import Image from 'lucide-solid/icons/image'
import { createMemo, Show } from 'solid-js'
import { pickObject, pickString } from '~/lib/jsonPick'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { FileImageMessage } from '../../../results/FileImageMessage'
import { ImageResultList } from '../../../results/imageResult'
import { ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary, toolResultError } from '../../../toolStyles.css'
import { defineCodexRenderer } from '../defineRenderer'
import { codexGeneratedImage, codexViewedImage } from '../extractors/image'
import { extractItem } from '../renderHelpers'
import { parseCodexStatus } from '../status'
import { codexStatusTitle } from './statusTitle'

/**
 * Codex `imageGeneration`: the `image_gen` tool's result.
 *
 * `result` is a base64 PNG and is empty until the item completes, so an
 * in-progress row shows the header alone. `revisedPrompt` is the prompt the
 * model actually rendered from -- it often differs from what the user asked
 * for, which is exactly why it is worth showing beside the picture.
 *
 * `failure` is a tagged union whose only member today is
 * `{type:'usageLimitExceeded', limitId, resetsAt}`; the type tag is rendered
 * rather than switched on, so a new failure kind reads as itself instead of
 * vanishing.
 */
export const CodexImageGenerationRenderer = defineCodexRenderer({
  itemTypes: [CODEX_ITEM.IMAGE_GENERATION],
  render: (props) => {
    const image = createMemo(() => codexGeneratedImage(props.item))
    const status = () => parseCodexStatus(props.item.status)
    const revisedPrompt = () => pickString(props.item, 'revisedPrompt', undefined)
    const failure = () => pickString(pickObject(props.item, 'failure'), 'type', undefined)
    return (
      <ToolUseLayout
        icon={Image}
        toolName="ImageGeneration"
        title={codexStatusTitle('Generate image', status() === CODEX_STATUS.IN_PROGRESS ? '' : status())}
        context={props.context}
        alwaysVisible
      >
        <Show when={revisedPrompt()}>
          {prompt => <div class={toolInputSummary}>{prompt()}</div>}
        </Show>
        <Show when={image()}>
          {source => <ImageResultList sources={[source()]} title="Generate image" context={props.context} />}
        </Show>
        <Show when={failure()}>
          {reason => <div class={toolResultError}>{reason()}</div>}
        </Show>
      </ToolUseLayout>
    )
  },
})

/**
 * Codex `imageView`: the `view_image` tool.
 *
 * The item supplies a path. The shared image component loads that file through the worker.
 */
export const CodexImageViewRenderer = defineCodexRenderer({
  itemTypes: [CODEX_ITEM.IMAGE_VIEW],
  render: (props) => {
    const source = createMemo(() => codexViewedImage(props.item))
    const matches = (item: Record<string, unknown> | null) => !!props.item.id && item?.id === props.item.id && item.type === CODEX_ITEM.IMAGE_VIEW
    const hasRequest = () => props.context?.sources?.role() === 'result' && matches(extractItem(props.context?.sources?.request()?.parentObject))
    const role = () => props.context?.sources?.role() === 'opener' ? 'request' as const : 'result' as const
    return (
      <FileImageMessage source={source() ?? {}} role={role()} hasRequest={hasRequest()} context={props.context} />
    )
  },
})
