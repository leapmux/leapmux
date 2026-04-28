import type { Component, JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { FileEditDiffSource } from '../../../results/fileEditDiff'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { createMemo, For, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CommandResultBody } from '../../../results/commandResult'
import { FileEditDiffBody, fileEditHasDiff } from '../../../results/fileEditDiff'
import { ReadFileResultBody } from '../../../results/readFileResult'
import { toolResultContentPre } from '../../../toolStyles.css'
import { extractPiBash, piBashToCommandSource } from '../extractors/bash'
import { extractPiEdit, extractPiRead, extractPiWrite, resolvePiResultDiff } from '../extractors/fileEdit'
import { piExtractTool } from '../extractors/toolCommon'
import { PI_TOOL } from '../protocol'

interface RendererProps {
  parsed: unknown
  role: MessageRole
  context?: RenderContext
}

interface ToolResultProps {
  payload: Record<string, unknown>
  context?: RenderContext
}
type ToolResultRenderer = Component<ToolResultProps>

/**
 * Resolve `args` from the matching `tool_execution_start` payload, which
 * the chat store wires through `context.toolUseParsed`. Pi's
 * `tool_execution_end` event itself carries no args, so result renderers
 * that need the original input (e.g. Read needs `filePath` for syntax
 * highlighting) reach back into the start.
 */
function startArgsFor(context: RenderContext | undefined): Record<string, unknown> {
  return pickObject(context?.toolUseParsed?.parentObject, 'args') ?? {}
}

function startPayloadFor(context: RenderContext | undefined): Record<string, unknown> | null {
  const payload = context?.toolUseParsed?.parentObject
  return isObject(payload) ? payload : null
}

function renderDiffSources(sources: FileEditDiffSource[], context: RenderContext | undefined): JSX.Element {
  return (
    <For each={sources}>
      {source => <FileEditDiffBody source={source} view={context?.diffView?.() ?? 'unified'} />}
    </For>
  )
}

function PiBashResult(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const bash = createMemo(() => extractPiBash(props.payload))
  return (
    <Show when={bash()}>
      {b => <CommandResultBody source={piBashToCommandSource(b())} context={props.context} />}
    </Show>
  )
}

function PiReadResult(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const read = createMemo(() => extractPiRead(props.payload, startArgsFor(props.context)))
  return (
    <Show when={read()}>
      {r => <ReadFileResultBody source={r().source} context={props.context} />}
    </Show>
  )
}

/**
 * Generic Pi tool-result body — renders the result text in a preformatted
 * block. Used for grep/find/ls (search output) and any unknown tool.
 * Returns null when the result text is empty.
 */
function PiGenericResult(props: { payload: Record<string, unknown> }): JSX.Element {
  const text = createMemo(() => piExtractTool(props.payload)?.result?.text ?? '')
  return (
    <Show when={text()}>
      <pre class={toolResultContentPre}>{text()}</pre>
    </Show>
  )
}

/**
 * Shared body for Pi edit/write result rendering. The result row prefers Pi's
 * applied diff (in `result.details.diff`); when that's absent, falls back to
 * a per-tool source extracted from the linked `tool_execution_start`. On
 * error, falls back to the result text instead of rendering the attempted
 * input as a successful diff (matches Claude Code's behavior).
 */
function PiDiffToolResult(props: {
  payload: Record<string, unknown>
  context?: RenderContext
  /**
   * Per-tool fallback when the result envelope carries no diff and the
   * tool didn't error. Receives the linked tool_execution_start payload and
   * returns the diff sources to render (empty array hides the diff body).
   */
  fallbackSources: (start: Record<string, unknown> | null) => FileEditDiffSource[]
}): JSX.Element {
  const tool = createMemo(() => piExtractTool(props.payload))
  const isError = createMemo(() => tool()?.isError === true)
  const resultDiff = createMemo(() => isError()
    ? { source: null, rawDiff: '' }
    : resolvePiResultDiff(props.payload, startArgsFor(props.context)))
  const sources = createMemo(() => {
    const resultSource = resultDiff().source
    if (resultSource)
      return [resultSource]
    if (isError() || resultDiff().rawDiff)
      return []
    return props.fallbackSources(startPayloadFor(props.context))
  })
  const fallbackText = createMemo(() => isError()
    ? (tool()?.result?.text ?? '')
    : (resultDiff().rawDiff || tool()?.result?.text || ''))
  return (
    <Show
      when={sources().length > 0}
      fallback={(
        <Show when={fallbackText()}>
          <pre class={toolResultContentPre}>{fallbackText()}</pre>
        </Show>
      )}
    >
      {renderDiffSources(sources(), props.context)}
    </Show>
  )
}

const PiEditResult: ToolResultRenderer = props => (
  <PiDiffToolResult
    payload={props.payload}
    context={props.context}
    fallbackSources={start => extractPiEdit(start)?.sources.filter(fileEditHasDiff) ?? []}
  />
)

const PiWriteResult: ToolResultRenderer = props => (
  <PiDiffToolResult
    payload={props.payload}
    context={props.context}
    fallbackSources={(start) => {
      const src = extractPiWrite(start)
      return src && fileEditHasDiff(src) ? [src] : []
    }}
  />
)

/**
 * Per-tool result-body renderer. Pi's `tool_execution_end` carries
 * `{toolCallId, toolName, result, isError}` — no args. The matching
 * `tool_execution_start` (with args) is already shown in its own bubble;
 * this layer only renders the output. Unknown tools fall through to the
 * generic preformatted-text result.
 */
const TOOL_RESULT_RENDERERS: Record<string, ToolResultRenderer> = {
  [PI_TOOL.Bash]: PiBashResult,
  [PI_TOOL.Read]: PiReadResult,
  [PI_TOOL.Edit]: PiEditResult,
  [PI_TOOL.Write]: PiWriteResult,
}

const FallbackToolResultRenderer: ToolResultRenderer = props => <PiGenericResult payload={props.payload} />

export function PiToolResultRenderer(props: RendererProps): JSX.Element {
  const payload = createMemo(() => isObject(props.parsed) ? props.parsed : null)
  const toolName = createMemo(() => pickString(payload() ?? {}, 'toolName'))
  return (
    <Show when={payload()}>
      {p => (
        <Dynamic
          component={TOOL_RESULT_RENDERERS[toolName()] ?? FallbackToolResultRenderer}
          payload={p()}
          context={props.context}
        />
      )}
    </Show>
  )
}
