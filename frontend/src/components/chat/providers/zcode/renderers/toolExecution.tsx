import type { Component, JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Eye from 'lucide-solid/icons/eye'
import FilePen from 'lucide-solid/icons/file-pen'
import FilePlus from 'lucide-solid/icons/file-plus'
import FolderSearch from 'lucide-solid/icons/folder-search'
import Globe from 'lucide-solid/icons/globe'
import ListChecks from 'lucide-solid/icons/list-checks'
import Terminal from 'lucide-solid/icons/terminal'
import TextSearch from 'lucide-solid/icons/text-search'
import Wrench from 'lucide-solid/icons/wrench'
import { createMemo, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { relativizePath } from '~/lib/paths'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { AgentRequestMessage } from '../../../results/AgentRequestMessage'
import { McpToolMessage } from '../../../results/McpToolMessage'
import {
  CommandInputBody,
  CommandInputSummary,
  createCommandInputExpansionState,
} from '../../../results/multiLineCommandBody'
import { TodoListMessage } from '../../../todoListMessage'
import { ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary } from '../../../toolStyles.css'
import { renderBashTitle, renderEditTitle, renderGlobTitle, renderReadTitle, renderSearchTitle, renderUrlTitle, renderWriteTitle } from '../../../toolTitleRenderers'
import { MarkdownPlanLayout } from '../../../widgets/MarkdownPlanLayout'
import { extractZCodeBash } from '../extractors/bash'
import { zcodeResultDisplay } from '../extractors/display'
import { zcodeFilePath } from '../extractors/fileEdit'
import { zcodeExtractTool, zcodeRow, zcodeRowFrom, zcodeTodoListFromInput, zcodeToolInput } from '../extractors/toolCommon'
import { ZCODE_WEB_FETCH } from '../protocol'

interface ToolProps {
  parsed: unknown
  context?: RenderContext
}
type ZCodeToolRenderer = Component<ToolProps>

function ZCodeBashRenderer(props: ToolProps): JSX.Element {
  const bash = createMemo(() => extractZCodeBash(zcodeRowFrom(props)))
  const command = () => bash()?.command ?? ''
  const title = () => renderBashTitle(bash()?.description, command()) || 'Run command'
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.TOOL_USE_LAYOUT)
  const { commandExpandable, setSummaryOverflows } = createCommandInputExpansionState(command)
  return (
    <ToolUseLayout
      icon={Terminal}
      toolName={ZCODE_TOOL.Bash}
      title={title()}
      summary={
        expanded() && commandExpandable()
          ? undefined
          : (
              <CommandInputSummary
                collapsed={!expanded()}
                command={command()}
                context={props.context}
                onOverflowChange={setSummaryOverflows}
              />
            )
      }
      context={props.context}
      expanded={expanded()}
      onToggleExpand={commandExpandable() ? () => setExpanded(v => !v) : undefined}
      expandLabel="Show full command"
    >
      <Show when={expanded() && commandExpandable()}>
        <CommandInputBody command={command()} context={props.context} />
      </Show>
    </ToolUseLayout>
  )
}

function ZCodeReadRenderer(props: ToolProps): JSX.Element {
  const input = createMemo(() => zcodeToolInput(zcodeRowFrom(props)))
  const path = createMemo(() => zcodeFilePath(zcodeRowFrom(props)))
  return (
    <ToolUseLayout
      icon={Eye}
      toolName={ZCODE_TOOL.Read}
      title={renderReadTitle(path(), pickNumber(input(), 'offset', undefined), pickNumber(input(), 'limit', undefined), props.context?.workingDir, props.context?.homeDir) || 'Read'}
      context={props.context}
      alwaysVisible
    />
  )
}

function ZCodeWriteRenderer(props: ToolProps): JSX.Element {
  const path = createMemo(() => zcodeFilePath(zcodeRowFrom(props)))
  const input = createMemo(() => zcodeToolInput(zcodeRowFrom(props)))
  return (
    <ToolUseLayout
      icon={FilePlus}
      toolName={ZCODE_TOOL.Write}
      title={renderWriteTitle(path(), pickString(input(), 'content'), props.context?.workingDir, props.context?.homeDir) || 'Write'}
      context={props.context}
      alwaysVisible
    />
  )
}

function ZCodeEditRenderer(props: ToolProps): JSX.Element {
  const path = createMemo(() => zcodeFilePath(zcodeRowFrom(props)))
  const input = createMemo(() => zcodeToolInput(zcodeRowFrom(props)))
  return (
    <ToolUseLayout
      icon={FilePen}
      toolName={ZCODE_TOOL.Edit}
      title={renderEditTitle(path(), pickString(input(), 'old_string', undefined), pickString(input(), 'new_string', undefined), input().replace_all === true, props.context?.workingDir, props.context?.homeDir) || 'Edit'}
      context={props.context}
      alwaysVisible
    />
  )
}

/**
 * A ZCode subagent spawn. The child's own transcript holds its work, so this row
 * states only what it was asked to do.
 */
function ZCodeAgentRenderer(props: ToolProps): JSX.Element {
  const input = createMemo(() => zcodeToolInput(zcodeRowFrom(props)))
  const description = () => pickString(input(), 'description')
  const prompt = () => pickString(input(), 'prompt')
  const hasResult = () => {
    const request = zcodeExtractTool(props.parsed)
    const result = zcodeExtractTool(props.context?.sources?.result()?.parentObject)
    return !!request?.toolCallId && result?.toolCallId === request.toolCallId && (!!result.result || result.isError)
  }
  return <AgentRequestMessage source={{ toolName: ZCODE_TOOL.Agent, description: description(), agentType: pickString(input(), 'subagent_type'), prompt: prompt() }} hasResult={hasResult()} context={props.context} />
}

/** Show the requested checklist until the matching successful result supplies its body. */
function ZCodeTodoWriteRenderer(props: ToolProps): JSX.Element {
  const source = createMemo(() =>
    zcodeTodoListFromInput(zcodeToolInput(zcodeRowFrom(props))))
  const hasResult = () => {
    const result = zcodeExtractTool(props.context?.sources?.result()?.parentObject)
    const request = zcodeExtractTool(props.parsed)
    return !!request?.toolCallId && result?.toolCallId === request.toolCallId && !!result.result && !result.isError
  }
  return (
    <Show when={source()} fallback={<ZCodeGenericToolRenderer parsed={props.parsed} context={props.context} />}>
      {resolved => <TodoListMessage source={resolved()} showBody={!hasResult()} context={props.context} />}
    </Show>
  )
}

const GENERIC_TOOL_ICONS: Record<string, typeof Wrench> = {
  [ZCODE_TOOL.Grep]: TextSearch,
  [ZCODE_TOOL.Glob]: FolderSearch,
  [ZCODE_TOOL.TaskOutput]: ListChecks,
  [ZCODE_WEB_FETCH]: Globe,
}

/**
 * The fallback keeps the tool name and arguments.
 * Known search and fetch tools use the shared title format.
 */
function ZCodeGenericToolRenderer(props: ToolProps): JSX.Element {
  const row = createMemo(() => zcodeRowFrom(props))
  const toolName = () => row().toolName || 'tool'
  const input = createMemo(() => zcodeToolInput(row()))
  const mcpSource = createMemo(() => {
    const current = zcodeExtractTool(row().parsed)
    const result = props.context?.sources?.result()
    const completed = zcodeExtractTool(result?.parentObject)
    if (!current || completed?.toolCallId !== current.toolCallId || !completed.result)
      return null
    const display = zcodeResultDisplay(zcodeRow(result?.parentObject, row().spanType, props.context?.sources?.current(), result?.supplementalContent))
    return display?.kind === 'mcp' ? { ...display.source, argsJson: display.source.argsJson || prettifyArgsJson(input()) } : null
  })
  const title = createMemo(() => {
    const args = input()
    const context = props.context
    if (toolName() === ZCODE_TOOL.Grep)
      return renderSearchTitle(pickString(args, 'pattern'), undefined, context?.workingDir, context?.homeDir) || toolName()
    if (toolName() === ZCODE_TOOL.Glob)
      return renderGlobTitle(pickString(args, 'pattern'), pickString(args, 'path'), context?.workingDir, context?.homeDir) || toolName()
    if (toolName() === ZCODE_WEB_FETCH)
      return renderUrlTitle(pickString(args, 'url')) || toolName()
    return toolName()
  })
  const summary = createMemo(() => {
    const value = input()
    if (toolName() === ZCODE_TOOL.Glob || toolName() === ZCODE_TOOL.Grep || toolName() === ZCODE_WEB_FETCH)
      return ''
    if (Object.keys(value).length === 0)
      return ''
    try {
      return JSON.stringify(value, null, 2)
    }
    catch {
      // A cyclic or otherwise unserializable input is not worth a broken row; the
      // title alone still says which tool ran.
      return ''
    }
  })
  return (
    <Show
      when={mcpSource()}
      fallback={(
        <ToolUseLayout
          icon={GENERIC_TOOL_ICONS[toolName()] ?? Wrench}
          toolName={toolName()}
          title={title()}
          summary={(
            <>
              <Show when={toolName() === ZCODE_TOOL.Grep && pickString(input(), 'path')}>
                {path => <div class={toolInputSummary}>{relativizePath(path(), props.context?.workingDir, props.context?.homeDir)}</div>}
              </Show>
              <Show when={summary()}>
                <pre class={toolInputSummary}>{summary()}</pre>
              </Show>
            </>
          )}
          context={props.context}
          alwaysVisible
        />
      )}
    >
      {source => <McpToolMessage source={source()} role="request" context={props.context} />}
    </Show>
  )
}

const DEDICATED_TOOL_RENDERERS: Record<string, ZCodeToolRenderer> = {
  [ZCODE_TOOL.ExitPlanMode]: props => <MarkdownPlanLayout toolName={ZCODE_TOOL.ExitPlanMode} title="Proposed Plan" planText={pickString(zcodeToolInput(zcodeRowFrom(props)), 'plan')} context={props.context} />,
  [ZCODE_TOOL.Bash]: ZCodeBashRenderer,
  [ZCODE_TOOL.Read]: ZCodeReadRenderer,
  [ZCODE_TOOL.Write]: ZCodeWriteRenderer,
  [ZCODE_TOOL.Edit]: ZCodeEditRenderer,
  [ZCODE_TOOL.Agent]: ZCodeAgentRenderer,
  [ZCODE_TOOL.TodoWrite]: ZCodeTodoWriteRenderer,
}

export function ZCodeToolExecutionRenderer(props: ToolProps): JSX.Element {
  const toolName = createMemo(() =>
    zcodeRowFrom(props).toolName)
  return (
    <Dynamic
      component={DEDICATED_TOOL_RENDERERS[toolName()] ?? ZCodeGenericToolRenderer}
      parsed={props.parsed}
      context={props.context}
    />
  )
}
