import type { Component, JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Eye from 'lucide-solid/icons/eye'
import FilePen from 'lucide-solid/icons/file-pen'
import FilePlus from 'lucide-solid/icons/file-plus'
import Folder from 'lucide-solid/icons/folder'
import FolderSearch from 'lucide-solid/icons/folder-search'
import Terminal from 'lucide-solid/icons/terminal'
import TextSearch from 'lucide-solid/icons/text-search'
import Wrench from 'lucide-solid/icons/wrench'
import { createMemo, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { relativizePath } from '~/lib/paths'
import { pluralize } from '~/lib/plural'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { McpToolMessage } from '../../../results/McpToolMessage'
import { CommandInputBody, CommandInputSummary, createCommandInputExpansionState } from '../../../results/multiLineCommandBody'
import { ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary } from '../../../toolStyles.css'
import { renderBashTitle, renderEditTitle, renderGlobTitle, renderReadTitle, renderSearchTitle, renderWriteTitle } from '../../../toolTitleRenderers'
import { extractPiCommand } from '../extractors/command'
import { extractPiRead, piEditsFromArgs } from '../extractors/fileEdit'
import { piGenericToolSource } from '../extractors/generic'
import { piExtractTool } from '../extractors/toolCommon'
import { PI_AGENT_TOOL, PI_POWERSHELL_TOOL, PI_SEARCH_TOOL } from '../protocol'
import { PiAgentRequest } from './agent'
import { PiPlanRequest } from './plan'
import { PiTodoRequest } from './todo'

interface RendererProps {
  parsed: unknown
  context?: RenderContext
}

export function PiCommandRenderer(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const bash = createMemo(() => extractPiCommand(props.payload))
  const command = () => bash()?.command ?? ''
  const language = () => props.payload.toolName === PI_POWERSHELL_TOOL ? 'powershell' as const : 'bash' as const
  const title = () => renderBashTitle('Run command', command()) || 'Run command'
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.TOOL_USE_LAYOUT)
  const { commandExpandable, setSummaryOverflows } = createCommandInputExpansionState(command)
  return (
    <ToolUseLayout
      icon={Terminal}
      toolName={props.payload.toolName === PI_POWERSHELL_TOOL ? 'PowerShell' : 'Bash'}
      title={title()}
      summary={
        expanded() && commandExpandable()
          ? undefined
          : (
              <CommandInputSummary
                language={language()}
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
        <CommandInputBody command={command()} language={language()} context={props.context} />
      </Show>
    </ToolUseLayout>
  )
}

function PiReadRenderer(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const read = createMemo(() => extractPiRead(props.payload))
  const path = () => read()?.source.filePath ?? ''
  return (
    <ToolUseLayout
      icon={Eye}
      toolName="Read"
      title={renderReadTitle(path(), read()?.offset ?? undefined, read()?.limit ?? undefined, props.context?.workingDir, props.context?.homeDir) ?? 'Read'}
      context={props.context}
      alwaysVisible
    />
  )
}

function PiWriteRenderer(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const tool = createMemo(() => piExtractTool(props.payload))
  const path = () => pickString(tool()?.args, 'path')
  return (
    <ToolUseLayout
      icon={FilePlus}
      toolName="Write"
      title={renderWriteTitle(path(), pickString(tool()?.args, 'content'), props.context?.workingDir, props.context?.homeDir) ?? 'Write'}
      context={props.context}
      alwaysVisible
    />
  )
}

function PiEditRenderer(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const tool = createMemo(() => piExtractTool(props.payload))
  const args = (): Record<string, unknown> => tool()?.args ?? {}
  const path = () => pickString(args(), 'path')
  const edits = createMemo(() => piEditsFromArgs(args()))
  return (
    <ToolUseLayout
      icon={FilePen}
      toolName="Edit"
      title={renderEditTitle(path(), edits().length === 1 ? edits()[0].oldText : undefined, edits().length === 1 ? edits()[0].newText : undefined, undefined, props.context?.workingDir, props.context?.homeDir) ?? 'Edit'}
      summary={(
        <Show when={edits().length > 1}><div class={toolInputSummary}>{pluralize(edits().length, 'edit')}</div></Show>
      )}
      context={props.context}
      alwaysVisible
    />
  )
}

/** Per-tool argument key + label used by the generic renderer's title. */
const GENERIC_TOOL_TITLE = new Map<string, { argKey: string, label: string }>([
  [PI_SEARCH_TOOL.Grep, { argKey: 'pattern', label: 'Grep' }],
  [PI_SEARCH_TOOL.Find, { argKey: 'pattern', label: 'Find' }],
  [PI_SEARCH_TOOL.List, { argKey: 'path', label: 'List' }],
])

interface GenericToolProps {
  payload: Record<string, unknown>
  toolName: string
  icon: typeof Wrench
  context?: RenderContext
}

function PiGenericToolRenderer(props: GenericToolProps): JSX.Element {
  const tool = createMemo(() => piExtractTool(props.payload))
  const title = () => {
    const meta = GENERIC_TOOL_TITLE.get(props.toolName)
    if (meta) {
      const args = tool()?.args
      const value = pickString(args, meta.argKey)
      if (props.toolName === PI_SEARCH_TOOL.Find)
        return renderGlobTitle(value, pickString(args, 'path'), props.context?.workingDir, props.context?.homeDir) ?? meta.label
      return meta.argKey === 'pattern'
        ? renderSearchTitle(value, undefined, props.context?.workingDir, props.context?.homeDir) ?? meta.label
        : renderReadTitle(value, undefined, undefined, props.context?.workingDir, props.context?.homeDir) ?? meta.label
    }
    return props.toolName
  }
  const genericSource = createMemo(() => !GENERIC_TOOL_TITLE.has(props.toolName) ? piGenericToolSource(props.payload, undefined, props.context?.sources?.result()) : null)
  return (
    <Show
      when={genericSource()}
      fallback={(
        <ToolUseLayout
          icon={props.icon}
          toolName={props.toolName}
          title={title()}
          summary={(
            <>
              <Show when={props.toolName === PI_SEARCH_TOOL.Grep && pickString(tool()?.args, 'path')}>
                {path => <div class={toolInputSummary}>{relativizePath(path(), props.context?.workingDir, props.context?.homeDir)}</div>}
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

interface ToolRendererProps {
  payload: Record<string, unknown>
  context?: RenderContext
}
type ToolRenderer = Component<ToolRendererProps>

/**
 * Tools with their own dedicated renderer. Anything not listed here renders
 * via the generic renderer with the icon from GENERIC_TOOL_ICONS (or Wrench
 * as the fallback) — so adding a new Pi tool name is data-only.
 */
const DEDICATED_TOOL_RENDERERS = new Map<string, ToolRenderer>([
  [PI_TOOL.PlanComplete, PiPlanRequest],
  [PI_TOOL.Agent, PiAgentRequest],
  [PI_TOOL.Todo, PiTodoRequest],
  [PI_TOOL.SubagentWorkflow, PiAgentRequest],
  [PI_AGENT_TOOL.GetResult, PiAgentRequest],
  [PI_AGENT_TOOL.Steer, PiAgentRequest],
  [PI_TOOL.Bash, PiCommandRenderer],
  [PI_POWERSHELL_TOOL, PiCommandRenderer],
  [PI_TOOL.Read, PiReadRenderer],
  [PI_TOOL.Write, PiWriteRenderer],
  [PI_TOOL.Edit, PiEditRenderer],
])

const GENERIC_TOOL_ICONS = new Map<string, typeof Wrench>([
  [PI_SEARCH_TOOL.Grep, TextSearch],
  [PI_SEARCH_TOOL.Find, FolderSearch],
  [PI_SEARCH_TOOL.List, Folder],
])

const FallbackToolExecutionRenderer: ToolRenderer = (props) => {
  const toolName = createMemo(() => pickString(props.payload, 'toolName') || 'tool')
  return (
    <PiGenericToolRenderer
      payload={props.payload}
      toolName={toolName()}
      icon={GENERIC_TOOL_ICONS.get(toolName()) ?? Wrench}
      context={props.context}
    />
  )
}

export function PiToolExecutionRenderer(props: RendererProps): JSX.Element {
  const payload = createMemo(() => isObject(props.parsed) ? props.parsed : null)
  const toolName = createMemo(() => pickString(payload() ?? {}, 'toolName'))
  return (
    <Show when={payload()}>
      {p => (
        <Dynamic
          component={DEDICATED_TOOL_RENDERERS.get(toolName()) ?? FallbackToolExecutionRenderer}
          payload={p()}
          context={props.context}
        />
      )}
    </Show>
  )
}
