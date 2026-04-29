/* eslint-disable solid/no-innerhtml -- HTML produced via shiki, not arbitrary user input */
import type { Component, JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Eye from 'lucide-solid/icons/eye'
import FileEdit from 'lucide-solid/icons/file-edit'
import FilePlus from 'lucide-solid/icons/file-plus'
import Folder from 'lucide-solid/icons/folder'
import Search from 'lucide-solid/icons/search'
import Terminal from 'lucide-solid/icons/terminal'
import Wrench from 'lucide-solid/icons/wrench'
import { createMemo, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { isObject, pickString } from '~/lib/jsonPick'
import { renderBashHighlight, ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary } from '../../../toolStyles.css'
import { renderBashTitle } from '../../../toolTitleRenderers'
import { extractPiBash } from '../extractors/bash'
import { extractPiRead } from '../extractors/fileEdit'
import { piExtractTool } from '../extractors/toolCommon'
import { PI_TOOL } from '../protocol'

interface RendererProps {
  parsed: unknown
  role: MessageRole
  context?: RenderContext
}

function PiBashRenderer(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const bash = createMemo(() => extractPiBash(props.payload))
  const command = () => bash()?.command ?? ''
  const title = () => renderBashTitle('Run command', command()) || 'Run command'
  return (
    <ToolUseLayout
      icon={Terminal}
      toolName="Bash"
      title={title()}
      summary={(
        <div class={toolInputSummary} innerHTML={renderBashHighlight(command())} />
      )}
      context={props.context}
      alwaysVisible
    />
  )
}

function PiReadRenderer(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const read = createMemo(() => extractPiRead(props.payload))
  const path = () => read()?.source.filePath ?? ''
  // Pi's `limit` is a line count, so the inclusive end is offset + limit - 1.
  const range = () => {
    const r = read()
    if (!r || (r.offset == null && r.limit == null))
      return null
    const start = r.offset ?? 1
    const end = r.limit != null ? start + r.limit - 1 : null
    return `range: lines ${start}-${end ?? 'end'}`
  }
  return (
    <ToolUseLayout
      icon={Eye}
      toolName="Read"
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

function PiWriteRenderer(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const tool = createMemo(() => piExtractTool(props.payload))
  const path = () => pickString(tool()?.args, 'path')
  return (
    <ToolUseLayout
      icon={FilePlus}
      toolName="Write"
      title={`Write ${path()}`}
      context={props.context}
      alwaysVisible
    />
  )
}

function PiEditRenderer(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const tool = createMemo(() => piExtractTool(props.payload))
  const args = (): Record<string, unknown> => tool()?.args ?? {}
  const path = () => pickString(args(), 'path')
  const editCount = () => {
    const edits = args().edits
    return Array.isArray(edits) ? edits.length : 0
  }
  return (
    <ToolUseLayout
      icon={FileEdit}
      toolName="Edit"
      title={`Edit ${path()}`}
      summary={(
        <div class={toolInputSummary}>{`${editCount()} edit(s)`}</div>
      )}
      context={props.context}
      alwaysVisible
    />
  )
}

/** Per-tool argument key + label used by the generic renderer's title. */
const GENERIC_TOOL_TITLE: Record<string, { argKey: string, label: string }> = {
  grep: { argKey: 'pattern', label: 'Grep' },
  find: { argKey: 'pattern', label: 'Find' },
  ls: { argKey: 'path', label: 'List' },
}

interface GenericToolProps {
  payload: Record<string, unknown>
  toolName: string
  icon: typeof Wrench
  context?: RenderContext
}

function PiGenericToolRenderer(props: GenericToolProps): JSX.Element {
  const tool = createMemo(() => piExtractTool(props.payload))
  const title = () => {
    const meta = GENERIC_TOOL_TITLE[props.toolName]
    if (meta)
      return `${meta.label} ${pickString(tool()?.args ?? {}, meta.argKey)}`
    return props.toolName
  }
  const summaryArgs = createMemo(() => {
    try {
      return JSON.stringify(tool()?.args ?? {}, null, 2)
    }
    catch {
      return ''
    }
  })
  return (
    <ToolUseLayout
      icon={props.icon}
      toolName={props.toolName}
      title={title()}
      summary={(
        <Show when={summaryArgs() && summaryArgs() !== '{}'}>
          <pre class={toolInputSummary}>{summaryArgs()}</pre>
        </Show>
      )}
      context={props.context}
      alwaysVisible
    />
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
const DEDICATED_TOOL_RENDERERS: Record<string, ToolRenderer> = {
  [PI_TOOL.Bash]: PiBashRenderer,
  [PI_TOOL.Read]: PiReadRenderer,
  [PI_TOOL.Write]: PiWriteRenderer,
  [PI_TOOL.Edit]: PiEditRenderer,
}

const GENERIC_TOOL_ICONS: Record<string, typeof Wrench> = {
  grep: Search,
  find: Folder,
  ls: Folder,
}

const FallbackToolExecutionRenderer: ToolRenderer = (props) => {
  const toolName = createMemo(() => pickString(props.payload, 'toolName') || 'tool')
  return (
    <PiGenericToolRenderer
      payload={props.payload}
      toolName={toolName()}
      icon={GENERIC_TOOL_ICONS[toolName()] ?? Wrench}
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
          component={DEDICATED_TOOL_RENDERERS[toolName()] ?? FallbackToolExecutionRenderer}
          payload={p()}
          context={props.context}
        />
      )}
    </Show>
  )
}
