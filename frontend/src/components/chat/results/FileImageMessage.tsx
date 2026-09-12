import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ImageResultSource } from '~/lib/imageBlocks'
import Eye from 'lucide-solid/icons/eye'
import { Show } from 'solid-js'
import { renderReadTitle } from '../toolTitleRenderers'
import { ToolMessageLayout } from '../widgets/ToolMessageLayout'
import { ImageResultView } from './imageResult'

/** Render a file image through the same header and image body as other file reads. */
export function FileImageMessage(props: {
  source: ImageResultSource
  role: 'request' | 'result'
  hasRequest?: boolean
  context?: RenderContext
}): JSX.Element {
  return (
    <ToolMessageLayout
      role={props.role}
      hasRequest={props.hasRequest}
      icon={Eye}
      toolName="View image"
      title={renderReadTitle(props.source.filePath, undefined, undefined, props.context?.workingDir, props.context?.homeDir) || 'View image'}
      context={props.context}
      alwaysVisible
    >
      <Show when={props.role === 'result'}>
        <ImageResultView source={props.source} title={props.source.filePath || 'Image'} context={props.context} />
      </Show>
    </ToolMessageLayout>
  )
}
