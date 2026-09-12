import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ACPToolAdapter } from '../toolPresentation'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, For, Show } from 'solid-js'
import { useCopyButton } from '~/hooks/useCopyButton'
import { prettifyJson } from '~/lib/jsonFormat'
import { pickFirstString, pickNumber, pickString } from '~/lib/jsonPick'
import { stripLeadingBlankLines } from '~/lib/normalizeProgressOutput'
import { relativizePath } from '~/lib/paths'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { AgentRequestMessage } from '../../../results/AgentRequestMessage'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from '../../../results/collapse'
import { CollapsibleContent } from '../../../results/CollapsibleContent'
import { FileEditDiffTitle } from '../../../results/fileEditDiff'
import { ImageResultList } from '../../../results/imageResult'
import { McpToolMessage } from '../../../results/McpToolMessage'
import { CommandInputBody, CommandInputSummary, createCommandInputExpansionState } from '../../../results/multiLineCommandBody'
import { renderToolBody } from '../../../results/toolBody'
import { ToolHeaderRow } from '../../../results/ToolStatusHeader'
import { useCollapsedLines } from '../../../results/useCollapsedLines'
import { toolInputSummary } from '../../../toolStyles.css'
import { renderAgentTitle, renderBashTitle, renderEditTitle, renderGlobTitle, renderReadTitle, renderSearchTitle, renderUrlTitle, renderWriteTitle } from '../../../toolTitleRenderers'
import { ToolMessageLayout } from '../../../widgets/ToolMessageLayout'
import { ACP_FILE_PATH_KEYS, ACP_NEW_TEXT_KEYS, ACP_OLD_TEXT_KEYS, acpInputPaths } from '../content'
import { acpImagesFromToolCall } from '../extractors/image'
import { acpToolFinished, acpToolPresentation, parsedACPToolCall, resolveACPToolCall } from '../toolPresentation'
import { acpToolOutputCollapsible } from '../toolResult'
import { kindIcon, kindLabel } from './helpers'

/** Render one request header and the shared body for its result. */
export function ToolCallUpdateMessage(props: {
  toolUse: Record<string, unknown>
  context?: RenderContext
  adapter?: ACPToolAdapter
}): JSX.Element {
  const current = () => props.context?.sources?.current()
  const request = () => props.context?.sources?.request()?.parentObject
  const tool = createMemo(() => resolveACPToolCall(props.toolUse, request()))
  const presentation = createMemo(() => acpToolPresentation(tool(), props.adapter, current()?.supplementalContent, current()?.completion))
  const pairedResult = () => !!tool().toolCallId && acpToolFinished(tool(), current()?.completion) && request()?.sessionUpdate === 'tool_call'
    && request()?.toolCallId === tool().toolCallId && !acpToolFinished(request()!, props.context?.sources?.request()?.completion)
  const headerPresentation = createMemo(() => {
    const result = props.context?.sources?.result()
    const completed = parsedACPToolCall(result?.parentObject)
    if (tool().sessionUpdate !== 'tool_call' || acpToolFinished(tool(), current()?.completion) || !completed || !acpToolFinished(completed, result?.completion)
      || !tool().toolCallId || completed.toolCallId !== tool().toolCallId) {
      return presentation()
    }
    return acpToolPresentation(resolveACPToolCall(completed, tool()), props.adapter, result?.supplementalContent, result?.completion)
  })
  const kind = () => headerPresentation().kind
  const input = () => headerPresentation().input
  const paths = createMemo(() => acpInputPaths(input()))
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
    const path = pickFirstString(args, ACP_FILE_PATH_KEYS)
    const context = props.context
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
      case 'read': return renderReadTitle(path, pickNumber(args, 'offset', undefined), pickNumber(args, 'limit', undefined), context?.workingDir, context?.homeDir) || model.title
      case 'list': return renderReadTitle(path || '.', undefined, undefined, context?.workingDir, context?.homeDir) || model.title
      case 'glob': return renderGlobTitle(pickString(args, 'pattern'), path, context?.workingDir, context?.homeDir) || model.title
      case 'grep': return renderSearchTitle(pickString(args, 'pattern'), undefined, context?.workingDir, context?.homeDir) || model.title
      case 'search': return renderSearchTitle(pickString(args, 'pattern') || pickString(args, 'query'), path, context?.workingDir, context?.homeDir) || model.title
      case 'edit':
      case 'write':
      case 'delete': return renderEditTitle(path, pickFirstString(args, ACP_OLD_TEXT_KEYS), pickFirstString(args, ACP_NEW_TEXT_KEYS), undefined, context?.workingDir, context?.homeDir) || model.title
      case 'fetch': return renderUrlTitle(pickString(args, 'url')) || model.title
      default: return model.title
    }
  }
  const images = createMemo(() => acpImagesFromToolCall({ ...tool(), rawInput: presentation().input }))
  const status = () => tool().status
  const genericInput = createMemo(() => {
    if (presentation().inputText)
      return presentation().inputText!
    if (['mcp', 'todo', 'markdown'].includes(body().type) || kind() === 'execute' || ['agent', 'read', 'list', 'edit', 'write', 'delete', 'search', 'glob', 'grep', 'fetch'].includes(kind()))
      return ''
    return Object.keys(input()).length > 0 ? prettifyJson(input()) : ''
  })
  const collapsedInput = useCollapsedLines({ text: genericInput, expanded })
  const expandable = () => commandExpandable() || acpToolOutputCollapsible(presentation()) || hasMoreLinesThan(genericInput(), COLLAPSED_RESULT_ROWS)
  const mcpSource = createMemo(() => {
    const content = acpToolFinished(tool(), current()?.completion) ? presentation().body : headerPresentation().body
    return content.type === 'mcp' ? content.source : undefined
  })
  const showBody = () => {
    if (acpToolFinished(tool(), current()?.completion))
      return true
    if (body().type === 'mcp')
      return false
    if (kind() === 'todo') {
      const result = parsedACPToolCall(props.context?.sources?.result()?.parentObject)
      return !(result?.toolCallId === tool().toolCallId && result?.status === 'completed' && headerPresentation().body.type === 'todo')
    }
    return true
  }

  const resultBody = () => (
    <>
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
      <Show when={body().type !== 'mcp' && images().length > 0}>
        <ImageResultList sources={images()} title={presentation().title} context={props.context} />
      </Show>
      <Show when={!props.context?.completionHeader && (status() === 'failed' || status() === 'cancelled') && !['agent', 'command', 'commands'].includes(body().type)}>
        <ToolHeaderRow icon={CircleAlert} title={status() === 'cancelled' ? 'Cancelled' : 'Failed'} />
      </Show>
    </>
  )

  const agentRequest = () => !acpToolFinished(tool(), current()?.completion) ? headerPresentation().agentRequest : undefined
  const hasAgentResult = () => {
    const result = props.context?.sources?.result()
    const completed = parsedACPToolCall(result?.parentObject)
    return !!tool().toolCallId && !!completed && completed.toolCallId === tool().toolCallId && acpToolFinished(completed, result?.completion)
  }

  return (
    <Show
      when={mcpSource()}
      fallback={(
        <Show
          when={agentRequest()}
          fallback={(
            <ToolMessageLayout
              role={acpToolFinished(tool(), current()?.completion) ? 'result' : 'request'}
              hasRequest={pairedResult()}
              icon={kindIcon(kind())}
              toolName={headerPresentation().label || kindLabel(kind())}
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
                  <Show when={!acpToolFinished(tool(), current()?.completion) && (headerPresentation().requestedChanges?.length ?? 0) > 1}>
                    <For each={headerPresentation().requestedChanges}>
                      {source => (
                        <div class={toolInputSummary}>
                          <FileEditDiffTitle source={{ ...source, operation: undefined }} context={props.context} />
                          <Show when={source.operation === 'delete'}> (delete)</Show>
                        </div>
                      )}
                    </For>
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
          {source => <AgentRequestMessage source={source()} hasResult={hasAgentResult()} context={props.context}>{resultBody()}</AgentRequestMessage>}
        </Show>
      )}
    >
      {source => <McpToolMessage source={source()} role={acpToolFinished(tool(), current()?.completion) ? 'result' : 'request'} hasRequest={pairedResult()} failureLabel={tool().status === 'cancelled' ? 'Cancelled' : undefined} context={props.context} />}
    </Show>
  )
}

export function acpToolCallUpdateRenderer(toolUse: Record<string, unknown>, context?: RenderContext, adapter?: ACPToolAdapter): JSX.Element {
  return <ToolCallUpdateMessage toolUse={toolUse} context={context} adapter={adapter} />
}
