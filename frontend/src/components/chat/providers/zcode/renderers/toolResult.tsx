import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ZCodeResultBody } from '../extractors/result'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import { createMemo, Show } from 'solid-js'
import { getToolResultExpanded } from '../../../messageRenderers'
import { AgentResultBody } from '../../../results/agentResult'
import { CollapsibleContent } from '../../../results/CollapsibleContent'
import { CommandResultBody } from '../../../results/commandResult'
import { FileEditDiffBody } from '../../../results/fileEditDiff'
import { ImageResultList } from '../../../results/imageResult'
import { ReadFileResultBody } from '../../../results/readFileResult'
import { SearchResultBody } from '../../../results/searchResult'
import { ToolHeaderRow } from '../../../results/ToolStatusHeader'
import { useCollapsedLines } from '../../../results/useCollapsedLines'
import { WebFetchResultBody } from '../../../results/webFetchResult'
import { TodoListMessage } from '../../../todoListMessage'
import { toolInputSummary } from '../../../toolStyles.css'
import { zcodeToolResultImages } from '../extractors/image'
import { zcodeResultPresentation } from '../extractors/result'
import { zcodePairedRequest, zcodeRowFrom } from '../extractors/toolCommon'
import { ZCodeDisplayBody } from './display'

function ZCodeTextResult(props: { text: string, context?: RenderContext }): JSX.Element {
  const collapsed = useCollapsedLines({ text: () => props.text, expanded: () => getToolResultExpanded(props.context) })
  return <Show when={props.text}><CollapsibleContent kind="ansi-or-pre" text={props.text} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} context={props.context} /></Show>
}

function renderBody(body: ZCodeResultBody, hasRequest: boolean, context?: RenderContext): JSX.Element {
  switch (body.kind) {
    case 'agent': return <AgentResultBody source={body.source} context={context} />
    case 'todo': return <TodoListMessage source={body.source} role="result" hasRequest={hasRequest} context={context} />
    case 'command': return <CommandResultBody source={body.source} context={context} />
    case 'diff': return <FileEditDiffBody source={body.source} view={context?.diffView?.() ?? 'unified'} context={context} />
    case 'read': return <ReadFileResultBody source={body.source} context={context} />
    case 'search': return <SearchResultBody source={body.source} context={context} />
    case 'fetch': return <WebFetchResultBody source={body.source} context={context} />
    case 'display': return <ZCodeDisplayBody source={body.source} hasRequest={hasRequest} context={context} />
    case 'text': return <ZCodeTextResult text={body.text} context={context} />
  }
}

/** Resolve tool identity and arguments before selecting shared result components. */
export function ZCodeToolResultRenderer(props: { parsed: unknown, context?: RenderContext }): JSX.Element {
  const row = createMemo(() => zcodeRowFrom(props))
  const presentation = createMemo(() => zcodeResultPresentation(row()))
  const images = createMemo(() => zcodeToolResultImages(row()))
  const imagesInBody = () => {
    const body = presentation()?.body
    return body?.kind === 'display' && body.source.kind === 'mcp'
  }
  return (
    <>
      <Show when={presentation()}>
        {result => (
          <>
            <Show when={!props.context?.completionHeader && result().failed && result().body.kind !== 'command' && result().body.kind !== 'display' && result().body.kind !== 'agent'}>
              <ToolHeaderRow icon={CircleAlert} title="Failed" />
            </Show>
            {renderBody(result().body, !!zcodePairedRequest(row().parsed, row().toolUseParsed), props.context)}
            <Show when={result().truncated && result().body.kind !== 'search'}>
              <div class={toolInputSummary}>Result truncated</div>
            </Show>
          </>
        )}
      </Show>
      <Show when={!imagesInBody()}>
        <ImageResultList sources={images()} title={row().toolName || 'Tool image'} context={props.context} />
      </Show>
    </>
  )
}
