import type { Component, JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Bot from 'lucide-solid/icons/bot'
import Eye from 'lucide-solid/icons/eye'
import FileEdit from 'lucide-solid/icons/file-edit'
import FilePlus from 'lucide-solid/icons/file-plus'
import Folder from 'lucide-solid/icons/folder'
import ListChecks from 'lucide-solid/icons/list-checks'
import Search from 'lucide-solid/icons/search'
import Terminal from 'lucide-solid/icons/terminal'
import Wrench from 'lucide-solid/icons/wrench'
import { createMemo, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import {
  CommandInputBody,
  CommandInputSummary,
  createCommandInputExpansionState,
} from '../../../results/multiLineCommandBody'
import { TodoListMessage } from '../../../todoListMessage'
import { ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary } from '../../../toolStyles.css'
import { renderBashTitle } from '../../../toolTitleRenderers'
import { extractZCodeBash } from '../extractors/bash'
import { zcodeFilePath } from '../extractors/fileEdit'
import { zcodeRowFrom, zcodeTodoListFromInput, zcodeToolInput } from '../extractors/toolCommon'

interface ToolProps {
  parsed: unknown
  context?: RenderContext
}
type ZCodeToolRenderer = Component<ToolProps>

function ZCodeBashRenderer(props: ToolProps): JSX.Element {
  const bash = createMemo(() => extractZCodeBash(zcodeRowFrom(props)))
  const command = () => bash()?.command ?? ''
  const title = () => renderBashTitle('Run command', command()) || 'Run command'
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
  // ZCode's `limit` is a line COUNT, so the inclusive last line is offset + limit - 1.
  const range = createMemo(() => {
    const offset = input().offset
    const limit = input().limit
    if (typeof offset !== 'number' && typeof limit !== 'number')
      return null
    const start = typeof offset === 'number' ? offset : 1
    const end = typeof limit === 'number' ? start + limit - 1 : null
    return `range: lines ${start}-${end ?? 'end'}`
  })
  return (
    <ToolUseLayout
      icon={Eye}
      toolName={ZCODE_TOOL.Read}
      title={`Read ${path()}`}
      summary={(
        <Show when={range()}>
          <div class={toolInputSummary}>{range()}</div>
        </Show>
      )}
      context={props.context}
      alwaysVisible
    />
  )
}

function ZCodeWriteRenderer(props: ToolProps): JSX.Element {
  const path = createMemo(() => zcodeFilePath(zcodeRowFrom(props)))
  return (
    <ToolUseLayout
      icon={FilePlus}
      toolName={ZCODE_TOOL.Write}
      title={`Write ${path()}`}
      context={props.context}
      alwaysVisible
    />
  )
}

function ZCodeEditRenderer(props: ToolProps): JSX.Element {
  const path = createMemo(() => zcodeFilePath(zcodeRowFrom(props)))
  return (
    <ToolUseLayout
      icon={FileEdit}
      toolName={ZCODE_TOOL.Edit}
      title={`Edit ${path()}`}
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
  return (
    <ToolUseLayout
      icon={Bot}
      toolName={ZCODE_TOOL.Agent}
      title={description() ? `Agent: ${description()}` : 'Agent'}
      summary={(
        <Show when={prompt()}>
          <div class={toolInputSummary}>{prompt()}</div>
        </Show>
      )}
      context={props.context}
      alwaysVisible
    />
  )
}

/**
 * The to-do list, drawn as a checklist rather than as raw JSON.
 *
 * ZCode re-sends the WHOLE list on every call, so the row is a snapshot of the
 * list at that point in the transcript -- which is exactly what TodoListMessage
 * shows. The paired result row is hidden (see the plugin), because it repeats
 * nothing this row does not already say.
 */
function ZCodeTodoWriteRenderer(props: ToolProps): JSX.Element {
  const source = createMemo(() =>
    zcodeTodoListFromInput(zcodeToolInput(zcodeRowFrom(props))))
  return (
    <Show when={source()} fallback={<ZCodeGenericToolRenderer parsed={props.parsed} context={props.context} />}>
      {resolved => <TodoListMessage source={resolved()} context={props.context} />}
    </Show>
  )
}

/** Per-tool title key and label for the generic renderer. */
const GENERIC_TOOL_TITLE: Record<string, { inputKey: string, label: string }> = {
  [ZCODE_TOOL.Grep]: { inputKey: 'pattern', label: 'Grep' },
  [ZCODE_TOOL.Glob]: { inputKey: 'pattern', label: 'Glob' },
}

const GENERIC_TOOL_ICONS: Record<string, typeof Wrench> = {
  [ZCODE_TOOL.Grep]: Search,
  [ZCODE_TOOL.Glob]: Folder,
  [ZCODE_TOOL.TaskOutput]: ListChecks,
}

/**
 * The fallback tool row: a title from the per-tool key when one is known, and the
 * input pretty-printed as the summary. Adding a ZCode tool name to the two tables
 * above is therefore data-only.
 */
function ZCodeGenericToolRenderer(props: ToolProps): JSX.Element {
  const toolName = createMemo(() =>
    zcodeRowFrom(props).toolName || 'tool')
  const input = createMemo(() => zcodeToolInput(zcodeRowFrom(props)))
  const title = createMemo(() => {
    const meta = GENERIC_TOOL_TITLE[toolName()]
    return meta ? `${meta.label} ${pickString(input(), meta.inputKey)}` : toolName()
  })
  const summary = createMemo(() => {
    const value = input()
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
    <ToolUseLayout
      icon={GENERIC_TOOL_ICONS[toolName()] ?? Wrench}
      toolName={toolName()}
      title={title()}
      summary={(
        <Show when={summary()}>
          <pre class={toolInputSummary}>{summary()}</pre>
        </Show>
      )}
      context={props.context}
      alwaysVisible
    />
  )
}

const DEDICATED_TOOL_RENDERERS: Record<string, ZCodeToolRenderer> = {
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
