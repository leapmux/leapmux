import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { MessageUiKey } from '../messageUiKeys'
import type { ToolMessageSource } from './toolPresentation'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, For, Show } from 'solid-js'
import { toolInputPaths } from '~/components/chat/results/toolInputs'
import { useCopyButton } from '~/hooks/useCopyButton'
import { prettifyJson } from '~/lib/jsonFormat'
import { pickString } from '~/lib/jsonPick'
import { stripLeadingBlankLines } from '~/lib/normalizeProgressOutput'
import { relativizePath } from '~/lib/paths'
import { useSharedExpandedState } from '../messageRenderers'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { toolOutcomeLabel } from '../toolOutcomeLabel'
import { toolInputSummary } from '../toolStyles.css'
import { kindHasTitleRenderer, toolMessageTitle } from '../toolTitleRenderers'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { ToolMessageLayout } from '../widgets/ToolMessageLayout'
import { AgentRequestMessage } from './AgentRequestMessage'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './collapse'
import { CollapsibleContent } from './CollapsibleContent'
import { ImageResultList } from './imageResult'
import { McpToolCallBody } from './mcpToolCall'
import { McpToolMessage } from './McpToolMessage'
import { CommandInputBody, CommandInputSummary, createCommandInputExpansionState } from './multiLineCommandBody'
import { RequestedFileChanges } from './requestedFileChanges'
import { renderToolBody } from './toolBody'
import { toolKindIcon, toolKindLabel } from './toolKind'
import { ToolMetadata } from './ToolMetadata'
import { toolBodyRepeatsInput, toolBodyStatesOwnOutcome, toolOutputCollapsible } from './toolResultMeta'

import { ToolHeaderRow, ToolOutcomeHeader } from './ToolStatusHeader'
import { useCollapsedLines } from './useCollapsedLines'

/** Render provider tool data with one shared layout and interaction model. */
export function ToolMessage(props: {
  source: ToolMessageSource
  request?: ToolMessageSource
  result?: ToolMessageSource
  context?: RenderContext
}): JSX.Element {
  const matches = (related: ToolMessageSource | undefined) => props.source.id && related?.id === props.source.id ? related : undefined
  const request = () => matches(props.request)
  const result = () => matches(props.result)
  const finished = () => props.source.role === 'result'
  const presentation = () => props.source.presentation
  const pairedResult = () => finished() && request()?.role === 'request'
  const headerPresentation = () => props.source.role === 'request' && result()?.role === 'result'
    ? result()!.presentation
    : presentation()
  const kind = () => headerPresentation().kind
  const input = () => headerPresentation().input
  const paths = createMemo(() => toolInputPaths(input()))
  const command = () => kind() === 'execute' ? pickString(input(), 'command') : ''
  const commandLanguage = () => headerPresentation().commandLanguage
  const body = () => presentation().body
  const output = createMemo(() => stripLeadingBlankLines(presentation().output))
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
  const { copied, copy } = useCopyButton(command)
  const { commandExpandable, setSummaryOverflows } = createCommandInputExpansionState(command)
  const collapsed = useCollapsedLines({ text: output, expanded })

  // The local state supports isolated renders without a message-store host.
  //
  // The READ and the WRITE of the expanded flag move together. An override that
  // took the read alone left the inherited writer in place, so a body renderer
  // that toggled the flag wrote its own local signal while this override kept
  // answering every read -- the write then had no effect that anybody could see.
  const bodyContext = createMemo<RenderContext>(() => {
    const context: RenderContext = Object.create(props.context ?? null)
    Object.defineProperties(context, {
      getMessageUiState: {
        value: (key: MessageUiKey) => key === MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED
          ? expanded()
          : props.context?.getMessageUiState?.(key),
      },
      setMessageUiState: {
        value: (key: MessageUiKey, value: boolean) => {
          if (key === MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
            setExpanded(value)
          else
            props.context?.setMessageUiState?.(key, value)
        },
      },
    })
    return context
  })

  const images = () => props.source.images
  const additionalImageCount = () => presentation().additionalContent?.content.filter(item => item.type === 'image').length ?? 0
  const status = () => props.source.status
  const genericInput = createMemo(() => {
    if (presentation().inputText)
      return presentation().inputText!
    if (!toolBodyRepeatsInput(body()) || kindHasTitleRenderer(kind()))
      return ''
    return Object.keys(input()).length > 0 ? prettifyJson(input()) : ''
  })
  const collapsedInput = useCollapsedLines({ text: genericInput, expanded })
  const expandable = () => commandExpandable() || toolOutputCollapsible(presentation()) || hasMoreLinesThan(genericInput(), COLLAPSED_RESULT_ROWS)
  const mcpSource = createMemo(() => {
    const content = finished() ? presentation().body : headerPresentation().body
    return content.type === 'mcp' ? content.source : undefined
  })
  const showBody = () => finished() || (body().type !== 'mcp'
    && !(kind() === 'todo' && result()?.status === 'completed' && headerPresentation().body.type === 'todo'))
  const hasCompletedResult = () => result()?.role === 'result'

  const resultBody = () => (
    <>
      <Show when={presentation().metadata}>{items => <ToolMetadata items={items()} />}</Show>
      <For each={presentation().unresolvedTerminals}>{id => <ToolHeaderRow icon={Terminal} title={`Terminal ${id}`} />}</For>
      <Show when={!pairedResult() && expanded() && commandExpandable()}>
        <CommandInputBody command={command()} language={commandLanguage()} context={props.context} />
      </Show>
      <Show
        when={body().type !== 'text'}
        fallback={(
          <Show when={output()}>
            <CollapsibleContent kind="ansi-or-pre" text={output()} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} context={props.context} />
          </Show>
        )}
      >
        <Show when={showBody()}>{renderToolBody(body(), bodyContext())}</Show>
      </Show>
      <Show when={presentation().truncated}>
        <div class={toolInputSummary}>{TRUNCATION_NOTICE}</div>
      </Show>
      <Show when={presentation().requestedChanges?.length && (finished() || !hasCompletedResult())}>
        <RequestedFileChanges sources={presentation().requestedChanges!} context={bodyContext()} />
      </Show>
      <Show when={presentation().additionalContent}>
        {source => <McpToolCallBody source={source()} context={bodyContext()} />}
      </Show>
      <Show when={body().type !== 'mcp' && images().length > 0}>
        <ImageResultList sources={images()} indexOffset={additionalImageCount()} title={presentation().title} context={props.context} />
      </Show>
      <ToolOutcomeHeader
        when={(status() === 'failed' || status() === 'cancelled') && !toolBodyStatesOwnOutcome(body())}
        icon={CircleAlert}
        title={toolOutcomeLabel(status() === 'cancelled' ? 'interrupted' : 'failed')}
        context={props.context}
      />
    </>
  )

  const agentRequest = () => !finished() ? headerPresentation().agentRequest : undefined

  return (
    <Show
      when={mcpSource()}
      fallback={(
        <Show
          when={agentRequest()}
          fallback={(
            <ToolMessageLayout
              role={finished() ? 'result' : 'request'}
              hasRequest={pairedResult()}
              icon={headerPresentation().icon ?? toolKindIcon(kind())}
              toolName={headerPresentation().label || toolKindLabel(kind())}
              title={toolMessageTitle(headerPresentation(), props.context)}
              summary={(
                <>
                  <Show when={command() && !(expanded() && commandExpandable())}>
                    <CommandInputSummary command={command()} language={commandLanguage()} context={props.context} collapsed={!expanded()} onOverflowChange={setSummaryOverflows} />
                  </Show>
                  <Show when={genericInput()}>
                    <div class={toolInputSummary}>
                      <CollapsibleContent kind="pre" text={genericInput()} display={collapsedInput.display()} isCollapsed={collapsedInput.isCollapsed()} context={props.context} />
                    </div>
                  </Show>
                  <Show when={kind() === 'grep' || (kind() === 'glob' && paths().length > 1)}>
                    <For each={paths()}>{path => <div class={toolInputSummary}>{relativizePath(path, props.context?.workingDir, props.context?.homeDir)}</div>}</For>
                  </Show>
                </>
              )}
              context={props.context}
              expanded={expanded()}
              onToggleExpand={expandable() ? () => setExpanded(value => !value) : undefined}
              expandLabel={commandExpandable() ? 'Show full command' : 'Expand output'}
              headerActions={{ onCopyContent: command() ? copy : undefined, contentCopied: copied(), copyContentLabel: 'Copy Command' }}
              alwaysVisible
            >
              {resultBody()}
            </ToolMessageLayout>
          )}
        >
          {source => <AgentRequestMessage source={source()} hasResult={hasCompletedResult()} context={props.context}>{resultBody()}</AgentRequestMessage>}
        </Show>
      )}
    >
      {source => <McpToolMessage source={source()} role={finished() ? 'result' : 'request'} hasRequest={pairedResult()} failureLabel={status() === 'cancelled' ? toolOutcomeLabel('interrupted') : undefined} additionalContent={presentation().additionalContent} context={props.context} />}
    </Show>
  )
}
