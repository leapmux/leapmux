import type { Component, JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { FileEditDiffSource } from '../../../results/fileEditDiff'
import { createMemo, For, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { AgentResultBody } from '../../../results/agentResult'
import { CommandResultBody } from '../../../results/commandResult'
import { DirectoryResultBody } from '../../../results/directoryResult'
import { FileEditDiffBody, fileEditHasDiff } from '../../../results/fileEditDiff'
import { ImageResultList } from '../../../results/imageResult'
import { McpToolMessage } from '../../../results/McpToolMessage'
import { ReadFileResultBody } from '../../../results/readFileResult'
import { SearchResultBody } from '../../../results/searchResult'
import { ToolResultMessage } from '../../../toolRenderers'
import { toolResultContentPre } from '../../../toolStyles.css'
import { extractPiCommand, piCommandSource } from '../extractors/command'
import { piSubagentNotificationSources } from '../extractors/customMessage'
import { extractPiEdit, extractPiRead, extractPiWrite, resolvePiResultDiff } from '../extractors/fileEdit'
import { piGenericToolSource } from '../extractors/generic'
import { piToolResultImages } from '../extractors/image'
import { extractPiSearch } from '../extractors/search'
import { piExtractTool, piPairedRequest } from '../extractors/toolCommon'
import { PI_AGENT_TOOL, PI_POWERSHELL_TOOL, PI_SEARCH_TOOL } from '../protocol'
import { PiAgentResult } from './agent'
import { PiPlanResult } from './plan'
import { PiTodoResult } from './todo'

interface RendererProps {
  parsed: unknown
  context?: RenderContext
}

interface ToolResultProps {
  payload: Record<string, unknown>
  context?: RenderContext
}
type ToolResultRenderer = Component<ToolResultProps>

/**
 * Resolve `args` from the matching `tool_execution_start` payload, which
 * the chat store wires through `context.sources?.request()`. Pi's
 * `tool_execution_end` event itself carries no args, so result renderers
 * that need the original input (e.g. Read needs `filePath` for syntax
 * highlighting) reach back into the start.
 */
function startArgsFor(payload: Record<string, unknown>, context: RenderContext | undefined): Record<string, unknown> {
  return pickObject(startPayloadFor(payload, context), 'args') ?? {}
}

function startPayloadFor(payload: Record<string, unknown>, context: RenderContext | undefined): Record<string, unknown> | null {
  return piPairedRequest(payload, context?.sources?.request())?.parentObject ?? null
}

function renderDiffSources(sources: FileEditDiffSource[], context: RenderContext | undefined): JSX.Element {
  return (
    <For each={sources}>
      {source => <FileEditDiffBody source={source} view={context?.diffView?.() ?? 'unified'} context={context} />}
    </For>
  )
}

function PiBashResult(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const bash = createMemo(() => extractPiCommand(props.payload))
  return (
    <Show when={bash()}>
      {b => <CommandResultBody source={piCommandSource(b())} context={props.context} />}
    </Show>
  )
}

function PiReadResult(props: { payload: Record<string, unknown>, context?: RenderContext }): JSX.Element {
  const read = createMemo(() => extractPiRead(props.payload, startArgsFor(props.payload, props.context)))
  return (
    <Show when={read()}>
      {r => <ReadFileResultBody source={r().source} context={props.context} />}
    </Show>
  )
}

function PiSearchResult(props: ToolResultProps): JSX.Element {
  const source = createMemo(() => extractPiSearch(props.payload))
  return (
    <Show when={source()}>
      {value => (
        <Show when={piExtractTool(props.payload)?.toolName === PI_SEARCH_TOOL.List} fallback={<SearchResultBody source={value()} context={props.context} />}>
          <DirectoryResultBody source={{ entries: value().filenames.map(path => ({ path })), truncated: value().truncated, notice: value().notice }} context={props.context} />
        </Show>
      )}
    </Show>
  )
}

/**
 * Render unknown Pi tools with the shared rich-content component.
 * Preserve structured details and non-text content on errors.
 */
function PiGenericResult(props: ToolResultProps): JSX.Element {
  const source = createMemo(() => piGenericToolSource(props.payload, props.context?.sources?.request()))
  return <Show when={source()}>{value => <McpToolMessage source={value()} role="result" hasRequest={!!startPayloadFor(props.payload, props.context)} context={props.context} />}</Show>
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
    : resolvePiResultDiff(props.payload, startArgsFor(props.payload, props.context)))
  const sources = createMemo(() => {
    const resultSource = resultDiff().source
    if (resultSource)
      return [resultSource]
    if (isError() || resultDiff().rawDiff)
      return []
    return props.fallbackSources(startPayloadFor(props.payload, props.context))
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

/** Each result selects one renderer for its images and errors. */
interface ToolResultPresentation {
  component: ToolResultRenderer
  images: 'shared' | 'body'
  errors: 'shared' | 'body'
}

const TOOL_RESULT_RENDERERS = new Map<string, ToolResultPresentation>([
  [PI_TOOL.PlanComplete, { component: PiPlanResult, images: 'shared', errors: 'shared' }],
  [PI_TOOL.Agent, { component: PiAgentResult, images: 'shared', errors: 'body' }],
  [PI_TOOL.Todo, { component: PiTodoResult, images: 'body', errors: 'body' }],
  [PI_TOOL.SubagentWorkflow, { component: PiAgentResult, images: 'shared', errors: 'body' }],
  [PI_AGENT_TOOL.GetResult, { component: PiAgentResult, images: 'shared', errors: 'body' }],
  [PI_AGENT_TOOL.Steer, { component: PiAgentResult, images: 'shared', errors: 'body' }],
  [PI_TOOL.Bash, { component: PiBashResult, images: 'shared', errors: 'body' }],
  [PI_POWERSHELL_TOOL, { component: PiBashResult, images: 'shared', errors: 'body' }],
  [PI_TOOL.Read, { component: PiReadResult, images: 'shared', errors: 'shared' }],
  [PI_TOOL.Edit, { component: PiEditResult, images: 'shared', errors: 'shared' }],
  [PI_TOOL.Write, { component: PiWriteResult, images: 'shared', errors: 'shared' }],
  [PI_SEARCH_TOOL.Grep, { component: PiSearchResult, images: 'shared', errors: 'shared' }],
  [PI_SEARCH_TOOL.Find, { component: PiSearchResult, images: 'shared', errors: 'shared' }],
  [PI_SEARCH_TOOL.List, { component: PiSearchResult, images: 'shared', errors: 'shared' }],
])

const FALLBACK_TOOL_RESULT: ToolResultPresentation = { component: PiGenericResult, images: 'body', errors: 'body' }

export function PiToolResultRenderer(props: RendererProps): JSX.Element {
  const payload = createMemo(() => isObject(props.parsed) ? props.parsed : null)
  const toolName = createMemo(() => pickString(payload() ?? {}, 'toolName'))
  const tool = createMemo(() => piExtractTool(payload()))
  const notifications = createMemo(() => payload() ? piSubagentNotificationSources(payload()!) : null)
  const presentation = createMemo(() => TOOL_RESULT_RENDERERS.get(toolName()) ?? FALLBACK_TOOL_RESULT)
  const sharedError = () => tool()?.isError && presentation().errors === 'shared'
  // Rich bodies render their own images. The shared error fallback keeps all images.
  const images = createMemo(() => sharedError() || presentation().images === 'shared' ? piToolResultImages(payload(), undefined, props.context?.sources?.request()) : [])
  return (
    <Show when={payload()}>
      {p => (
        <Show when={!notifications()} fallback={<For each={notifications()}>{source => <AgentResultBody source={source} context={props.context} />}</For>}>
          <Show
            when={!sharedError()}
            fallback={<ToolResultMessage resultContent={tool()?.result?.text ?? ''} isError context={props.context} />}
          >
            <Dynamic
              component={presentation().component}
              payload={p()}
              context={props.context}
            />
          </Show>
          <Show when={images().length > 0}>
            <ImageResultList sources={images()} title={toolName()} context={props.context} />
          </Show>
        </Show>
      )}
    </Show>
  )
}
