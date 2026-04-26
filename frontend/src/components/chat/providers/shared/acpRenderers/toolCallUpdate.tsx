/* eslint-disable solid/no-innerhtml -- HTML is produced via remark/shiki/ANSI, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import { createMemo, createSignal, Show } from 'solid-js'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { ACP_TOOL_KIND } from '~/types/toolMessages'
import { useSharedExpandedState } from '../../../messageRenderers'
import { CommandResultBody } from '../../../results/commandResult'
import { FileEditDiffBody, pickFileEditDiff } from '../../../results/fileEditDiff'
import { ReadFileResultBody } from '../../../results/readFileResult'
import { SearchResultBody } from '../../../results/searchResult'
import { ToolHeaderRow } from '../../../results/ToolStatusHeader'
import { WebFetchResultBody } from '../../../results/webFetchResult'
import { COLLAPSED_RESULT_ROWS, renderBashHighlight, stripLeadingBlankLines, ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary, toolResultCollapsed, toolResultContentAnsi, toolResultContentPre } from '../../../toolStyles.css'
import { renderReadTitle, renderSearchTitle } from '../../../toolTitleRenderers'
import { acpExecuteFromToolCall } from '../../acp-extractors/execute'
import { acpFileEditFromToolCallContent, acpFileEditFromToolCallRawInput } from '../../acp-extractors/fileEdit'
import { acpReadFromToolCall } from '../../acp-extractors/read'
import { acpSearchFromToolCall } from '../../acp-extractors/search'
import { acpWebFetchFromToolCall } from '../../acp-extractors/webFetch'
import { collectAcpToolText } from '../acpRendering'
import { kindIcon, kindLabel } from './helpers'

/** Extract text output from tool_call_update content array. */
function extractToolOutput(toolUse: Record<string, unknown>): { text: string, error: boolean } {
  const status = toolUse.status as string | undefined
  return { text: collectAcpToolText(toolUse), error: status === 'failed' }
}

function searchTitle(rawInput: Record<string, unknown> | undefined, fallbackTitle: string, context?: RenderContext): string | JSX.Element {
  const pattern = pickString(rawInput, 'pattern')
  const path = pickString(rawInput, 'path')
  return renderSearchTitle(pattern, path, context?.workingDir, context?.homeDir) || fallbackTitle
}

function readTitle(rawInput: Record<string, unknown> | undefined, fallbackTitle: string, context?: RenderContext): string | JSX.Element {
  const filePath = pickString(rawInput, 'filePath')
  const offset = pickNumber(rawInput, 'offset', 0)
  const limit = pickNumber(rawInput, 'limit', 0)
  return renderReadTitle(filePath, offset, limit, context?.workingDir, context?.homeDir) || fallbackTitle
}

/**
 * Inner component for ACP tool_call_update messages.
 * Handles all kinds (execute, search, read, edit, etc.) with kind-specific
 * title, summary, expand/collapse, and body rendering — mirroring Claude Code's
 * tool_use + tool_result combined in a single message.
 */
function ToolCallUpdateMessage(props: {
  toolUse: Record<string, unknown>
  context?: RenderContext
}): JSX.Element {
  const kind = () => props.toolUse.kind as string | undefined
  const rawInput = () => isObject(props.toolUse.rawInput) ? props.toolUse.rawInput as Record<string, unknown> : undefined

  const icon = () => kindIcon(kind())
  const command = () => pickString(rawInput(), 'command')
  const description = () => pickString(rawInput(), 'description')
  const fallbackTitle = () => description() || (props.toolUse.title as string | undefined) || kind() || 'Tool'

  // Memo: read 5+ times per render across the JSX below; without this,
  // `extractToolOutput` re-walks `props.toolUse.content` each access.
  const output = createMemo(() => extractToolOutput(props.toolUse))
  const outputText = () => stripLeadingBlankLines(output().text)

  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, 'opencode-tool-call-update')
  const [commandCopied, setCommandCopied] = createSignal(false)

  // Output collapsing
  const outputLines = () => outputText().split('\n')
  const isOutputCollapsed = () => !expanded() && outputLines().length > COLLAPSED_RESULT_ROWS
  const displayOutput = () => {
    if (!isOutputCollapsed())
      return outputText()
    return outputLines().slice(0, COLLAPSED_RESULT_ROWS).join('\n')
  }

  // Kind-specific title
  const title = (): string | JSX.Element => {
    switch (kind()) {
      case ACP_TOOL_KIND.SEARCH: return searchTitle(rawInput(), fallbackTitle(), props.context)
      case ACP_TOOL_KIND.READ: return readTitle(rawInput(), fallbackTitle(), props.context)
      default: return fallbackTitle()
    }
  }

  // Kind-specific summary
  const summary = (): JSX.Element | undefined => {
    switch (kind()) {
      case ACP_TOOL_KIND.EXECUTE: {
        const cmd = command()
        if (!cmd)
          return undefined
        return <div class={toolInputSummary} innerHTML={renderBashHighlight(cmd.split('\n')[0])} />
      }
      default:
        return undefined
    }
  }

  // Expand/collapse
  const isMultiLineCommand = () => command().includes('\n')
  const hasExpandable = () => isMultiLineCommand() || outputLines().length > COLLAPSED_RESULT_ROWS
  const expandLabel = () => isMultiLineCommand() ? 'Show full command' : 'Expand output'

  // Execute-specific: hide summary when expanded + multi-line command
  const displaySummary = () => expanded() && isMultiLineCommand() ? undefined : summary()

  // Execute kind: build a command source for the shared CommandResultBody.
  const executeSource = createMemo(() => kind() === ACP_TOOL_KIND.EXECUTE ? acpExecuteFromToolCall(props.toolUse) : null)
  const showExecuteView = () => executeSource() !== null

  // Diff view: prefer the update's own diff content; fall back to a diff
  // synthesized from the original tool_call rawInput so all providers share
  // the "result-diff or tool_use-diff" rule. Memoized so the content array
  // walk and rawInput inspection don't re-run on every JSX prop read.
  const effectiveDiff = createMemo(() => pickFileEditDiff(
    acpFileEditFromToolCallContent(props.toolUse.content),
    acpFileEditFromToolCallRawInput(kind(), rawInput()),
  ))

  // Read kind: surface a syntax-highlighted view when the raw output parses
  // as cat-n format. Null sources fall back to the generic text branch.
  const readSource = createMemo(() => kind() === ACP_TOOL_KIND.READ ? acpReadFromToolCall(props.toolUse) : null)
  const showReadView = () => readSource() !== null && readSource()!.lines !== null

  // Search kind: route through the shared SearchResultBody when the update
  // carries a recognizable shape.
  const searchSource = createMemo(() => kind() === ACP_TOOL_KIND.SEARCH ? acpSearchFromToolCall(props.toolUse) : null)
  const showSearchView = () => searchSource() !== null

  // Fetch kind: route through the shared WebFetchResultBody when the update
  // carries a recognizable HTTP shape; fall back to the generic text branch.
  const fetchSource = createMemo(() => kind() === ACP_TOOL_KIND.FETCH ? acpWebFetchFromToolCall(props.toolUse) : null)
  const showFetchView = () => fetchSource() !== null

  return (
    <ToolUseLayout
      icon={icon()}
      toolName={kindLabel(kind())}
      title={title()}
      summary={displaySummary()}
      context={props.context}
      expanded={expanded()}
      onToggleExpand={hasExpandable() ? () => setExpanded(v => !v) : undefined}
      expandLabel={expandLabel()}
      headerActions={{
        onCopyContent: command()
          ? () => {
              navigator.clipboard.writeText(command())
              setCommandCopied(true)
              setTimeout(setCommandCopied, 2000, false)
            }
          : undefined,
        contentCopied: commandCopied(),
        copyContentLabel: 'Copy Command',
      }}
      alwaysVisible
    >
      {/* Execute: full multi-line command (when expanded) */}
      <Show when={kind() === ACP_TOOL_KIND.EXECUTE && expanded() && isMultiLineCommand()}>
        <div class={toolResultContentAnsi} innerHTML={renderBashHighlight(command())} />
      </Show>

      {/* Execute: shared CommandResultBody (canonical status + output). */}
      <Show when={showExecuteView()}>
        <CommandResultBody source={executeSource()!} context={props.context} />
      </Show>

      {/* Diff view (edit kind) — picks result diff first, then tool_use input. */}
      <Show when={effectiveDiff()}>
        {src => <FileEditDiffBody source={src()} view={props.context?.diffView?.() ?? 'unified'} />}
      </Show>

      {/* Read kind: syntax-highlighted view when the output parses as cat-n. */}
      <Show when={showReadView()}>
        <ReadFileResultBody source={readSource()!} context={props.context} />
      </Show>

      {/* Search kind: shared SearchResultBody (matches summary). */}
      <Show when={showSearchView()}>
        <SearchResultBody source={searchSource()!} context={props.context} />
      </Show>

      {/* Fetch kind: shared WebFetchResultBody when the update carries an
          HTTP-status shape; otherwise falls back to the text branch below. */}
      <Show when={showFetchView()}>
        <WebFetchResultBody source={fetchSource()!} context={props.context} />
      </Show>

      {/* Text output (kinds without a dedicated body shown above) */}
      <Show when={outputText() && effectiveDiff() === null && !showReadView() && !showSearchView() && !showFetchView() && !showExecuteView()}>
        {containsAnsi(outputText())
          ? <div class={`${toolResultContentAnsi}${isOutputCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderAnsi(displayOutput())} />
          : <div class={`${toolResultContentPre}${isOutputCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>{displayOutput()}</div>}
      </Show>

      {/* Error indicator when no output text and no execute body to handle it */}
      <Show when={output().error && !outputText() && effectiveDiff() === null && !showExecuteView()}>
        <ToolHeaderRow icon={CircleAlert} title="Failed" />
      </Show>
    </ToolUseLayout>
  )
}

/** Render an ACP tool_call_update (completed/failed) — combined tool_use + tool_result. */
export function acpToolCallUpdateRenderer(toolUse: Record<string, unknown>, _role: MessageRole, context?: RenderContext): JSX.Element {
  return <ToolCallUpdateMessage toolUse={toolUse} context={context} />
}
