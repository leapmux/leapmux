import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ToolMessageSource } from './toolPresentation'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, For, Show } from 'solid-js'
import { TOOL_FILE_PATH_KEYS, TOOL_NEW_TEXT_KEYS, TOOL_OLD_TEXT_KEYS, toolInputPaths } from '~/components/chat/results/toolInputs'
import { useCopyButton } from '~/hooks/useCopyButton'
import { prettifyJson } from '~/lib/jsonFormat'
import { pickFirstString, pickNumber, pickString } from '~/lib/jsonPick'
import { stripLeadingBlankLines } from '~/lib/normalizeProgressOutput'
import { relativizePath } from '~/lib/paths'
import { useSharedExpandedState } from '../messageRenderers'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { toolOutcomeLabel } from '../toolOutcomeLabel'
import { toolInputSummary } from '../toolStyles.css'
import { renderAgentTitle, renderBashTitle, renderEditTitle, renderGlobTitle, renderReadTitle, renderSearchTitle, renderUrlTitle, renderWriteTitle } from '../toolTitleRenderers'
import { ToolMessageLayout } from '../widgets/ToolMessageLayout'
import { AgentRequestMessage } from './AgentRequestMessage'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './collapse'
import { CollapsibleContent } from './CollapsibleContent'
import { FileEditDiffTitle } from './fileEditDiff'
import { ImageResultList } from './imageResult'
import { McpToolCallBody } from './mcpToolCall'
import { McpToolMessage } from './McpToolMessage'
import { CommandInputBody, CommandInputSummary, createCommandInputExpansionState } from './multiLineCommandBody'
import { RequestedFileChanges } from './requestedFileChanges'
import { renderToolBody } from './toolBody'
import { toolKindIcon, toolKindLabel } from './toolKind'
import { ToolMetadata } from './ToolMetadata'
import { toolOutputCollapsible } from './toolResultMeta'

import { ToolHeaderRow } from './ToolStatusHeader'
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
  const body = () => presentation().body
  const output = createMemo(() => stripLeadingBlankLines(presentation().output))
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
  const { copied, copy } = useCopyButton(command)
  const { commandExpandable, setSummaryOverflows } = createCommandInputExpansionState(command)
  const collapsed = useCollapsedLines({ text: output, expanded })

  // The local state supports isolated renders without a message-store host.
  const bodyContext = createMemo<RenderContext>(() => {
    const context: RenderContext = Object.create(props.context ?? null)
    Object.defineProperty(context, 'getMessageUiState', {
      value: (key: Parameters<NonNullable<RenderContext['getMessageUiState']>>[0]) => key === MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED
        ? expanded()
        : props.context?.getMessageUiState?.(key),
    })
    return context
  })

  const title = (): JSX.Element => {
    const model = headerPresentation()
    const args = model.input
    const path = pickFirstString(args, TOOL_FILE_PATH_KEYS)
    const context = props.context
    // A read whose only path is the WORKING DIRECTORY has no file to name, and a
    // row titled with it reads as a bare ".". Cursor's `ReadLints` reaches here:
    // it declares ACP kind `read`, sends `title: "Read Lints"`, and gives the
    // working directory as its only location -- so the title it sent lost to a
    // path that says nothing. `list` keeps that path on purpose, because listing
    // the working directory IS what "." means there.
    const readPath = () => path && relativizePath(path, context?.workingDir, context?.homeDir) === '.' ? '' : path
    if (model.kind === 'write' && typeof args.content === 'string')
      return renderWriteTitle(path, args.content, context?.workingDir, context?.homeDir) || model.title
    const changes = model.body.type === 'diff' ? model.body.sources : model.requestedChanges
    if (changes?.length) {
      if (changes.length === 1)
        return <FileEditDiffTitle source={model.body.type === 'diff' ? changes[0] : { ...changes[0], operation: undefined }} context={context} />
      const paths = new Set(changes.map(source => source.filePath))
      return paths.size === 1
        ? `${changes.length} changes in ${relativizePath(changes[0].filePath, context?.workingDir, context?.homeDir)}`
        : `${paths.size} files${model.body.type === 'diff' ? ' changed' : ''}`
    }
    switch (model.kind) {
      case 'agent': return renderAgentTitle(model.title, model.agentRequest?.agentType)
      case 'execute': return renderBashTitle(pickString(args, 'description') || (model.title !== pickString(args, 'command') && model.title !== model.kind ? model.title : ''), pickString(args, 'command')) || model.title || 'Run command'
      case 'read': return renderReadTitle(readPath(), pickNumber(args, 'offset', undefined), pickNumber(args, 'limit', undefined), context?.workingDir, context?.homeDir) || model.title
      case 'list': return renderReadTitle(path || '.', undefined, undefined, context?.workingDir, context?.homeDir) || model.title
      case 'glob': return renderGlobTitle(pickString(args, 'pattern'), path, context?.workingDir, context?.homeDir) || model.title
      case 'grep': return renderSearchTitle(pickString(args, 'pattern'), undefined, context?.workingDir, context?.homeDir) || model.title
      case 'search': return renderSearchTitle(pickString(args, 'pattern') || pickString(args, 'query'), path, context?.workingDir, context?.homeDir) || model.title
      case 'edit':
      case 'write':
      case 'delete': return renderEditTitle(path, pickFirstString(args, TOOL_OLD_TEXT_KEYS), pickFirstString(args, TOOL_NEW_TEXT_KEYS), undefined, context?.workingDir, context?.homeDir) || model.title
      case 'fetch': return renderUrlTitle(pickString(args, 'url')) || model.title
      default: return model.title
    }
  }
  const images = () => props.source.images
  const additionalImageCount = () => presentation().additionalContent?.content.filter(item => item.type === 'image').length ?? 0
  const status = () => props.source.status
  const genericInput = createMemo(() => {
    if (presentation().inputText)
      return presentation().inputText!
    if (['mcp', 'todo', 'markdown'].includes(body().type) || kind() === 'execute' || ['agent', 'read', 'list', 'edit', 'write', 'delete', 'search', 'glob', 'grep', 'fetch'].includes(kind()))
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
        <CommandInputBody command={command()} context={props.context} />
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
      <Show when={presentation().requestedChanges?.length && (finished() || !hasCompletedResult())}>
        <RequestedFileChanges sources={presentation().requestedChanges!} context={bodyContext()} />
      </Show>
      <Show when={presentation().additionalContent}>
        {source => <McpToolCallBody source={source()} context={bodyContext()} />}
      </Show>
      <Show when={body().type !== 'mcp' && images().length > 0}>
        <ImageResultList sources={images()} indexOffset={additionalImageCount()} title={presentation().title} context={props.context} />
      </Show>
      <Show when={!props.context?.completionHeader && (status() === 'failed' || status() === 'cancelled') && !['agent', 'command', 'commands'].includes(body().type)}>
        <ToolHeaderRow icon={CircleAlert} title={toolOutcomeLabel(status() === 'cancelled' ? 'interrupted' : 'failed')} />
      </Show>
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
              icon={toolKindIcon(kind())}
              toolName={headerPresentation().label || toolKindLabel(kind())}
              title={title()}
              summary={(
                <>
                  <Show when={command() && !(expanded() && commandExpandable())}>
                    <CommandInputSummary command={command()} context={props.context} collapsed={!expanded()} onOverflowChange={setSummaryOverflows} />
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
      {source => <McpToolMessage source={source()} role={finished() ? 'result' : 'request'} hasRequest={pairedResult()} failureLabel={status() === 'cancelled' ? toolOutcomeLabel('interrupted') : undefined} context={props.context} />}
    </Show>
  )
}
