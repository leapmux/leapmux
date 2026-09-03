import Image from 'lucide-solid/icons/image'
import { createMemo, Show } from 'solid-js'
import { pickObject, pickString } from '~/lib/jsonPick'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { ImageResultList } from '../../../results/imageResult'
import { ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary, toolResultError } from '../../../toolStyles.css'
import { renderReadTitle } from '../../../toolTitleRenderers'
import { defineCodexRenderer } from '../defineRenderer'
import { codexGeneratedImage } from '../extractors/image'
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
 * The item is `{id, path}` and carries no pixels -- Codex attaches the image
 * to the model's context, not to the transcript. A header that states the file is
 * everything this row can say.
 */
export const CodexImageViewRenderer = defineCodexRenderer({
  itemTypes: [CODEX_ITEM.IMAGE_VIEW],
  render: (props) => {
    const path = () => pickString(props.item, 'path')
    return (
      <ToolUseLayout
        icon={Image}
        toolName="ViewImage"
        title={renderReadTitle(path(), undefined, undefined, props.context?.workingDir, props.context?.homeDir) ?? 'View image'}
        context={props.context}
        alwaysVisible
      />
    )
  },
})
