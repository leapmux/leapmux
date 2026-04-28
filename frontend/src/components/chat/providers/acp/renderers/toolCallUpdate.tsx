/* eslint-disable solid/no-innerhtml -- HTML is produced via remark/shiki/ANSI, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { CommandResultSource } from '../../../results/commandResult'
import type { FileEditDiffSource } from '../../../results/fileEditDiff'
import type { ReadFileResultSource } from '../../../results/readFileResult'
import type { SearchResultSource } from '../../../results/searchResult'
import type { WebFetchResultSource } from '../../../results/webFetchResult'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import { createMemo, Show } from 'solid-js'
import { useCopyButton } from '~/hooks/useCopyButton'
import { pickFirstString, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { ACP_TOOL_KIND } from '~/types/toolMessages'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { firstNonEmptyLine } from '../../../rendererUtils'
import { CollapsibleContent } from '../../../results/CollapsibleContent'
import { CommandResultBody } from '../../../results/commandResult'
import { FileEditDiffBody, pickFileEditDiff } from '../../../results/fileEditDiff'
import { isMultiLineCommand, MultiLineCommandBody } from '../../../results/multiLineCommandBody'
import { ReadFileResultBody } from '../../../results/readFileResult'
import { SearchResultBody } from '../../../results/searchResult'
import { ToolHeaderRow } from '../../../results/ToolStatusHeader'
import { hasMoreLinesThan, useCollapsedLines } from '../../../results/useCollapsedLines'
import { WebFetchResultBody } from '../../../results/webFetchResult'
import { COLLAPSED_RESULT_ROWS, renderBashHighlight, stripLeadingBlankLines, ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary } from '../../../toolStyles.css'
import { renderReadTitle, renderSearchTitle } from '../../../toolTitleRenderers'
import { acpExecuteFromToolCall } from '../extractors/execute'
import { acpFileEditFromToolCallContent, acpFileEditFromToolCallRawInput } from '../extractors/fileEdit'
import { acpReadFromToolCall } from '../extractors/read'
import { acpSearchFromToolCall } from '../extractors/search'
import { acpWebFetchFromToolCall } from '../extractors/webFetch'
import { ACP_FILE_PATH_KEYS, collectAcpToolText } from '../rendering'
import { kindIcon, kindLabel } from './helpers'

/**
 * Discriminated body shape for an ACP tool_call_update. Computed once per
 * render so only the active kind's extractor runs (rather than all five
 * gating themselves on `kind() === X`). `'none'` falls back to the generic
 * text branch.
 */
type AcpKindBody
  = | { kind: 'execute', source: CommandResultSource }
    | { kind: 'edit', diff: FileEditDiffSource }
    | { kind: 'read', source: ReadFileResultSource }
    | { kind: 'search', source: SearchResultSource }
    | { kind: 'fetch', source: WebFetchResultSource }
    | { kind: 'none' }

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
  const filePath = pickFirstString(rawInput, ACP_FILE_PATH_KEYS) ?? ''
  const offset = pickNumber(rawInput, 'offset', 0)
  const limit = pickNumber(rawInput, 'limit', 0)
  return renderReadTitle(filePath, offset, limit, context?.workingDir, context?.homeDir) || fallbackTitle
}

/**
 * Render the kind-specific body for a discriminated `AcpKindBody`. Returns
 * null for `'none'` so the caller can fall through to the generic text body.
 */
function renderKindBody(body: AcpKindBody, context: RenderContext | undefined): JSX.Element | null {
  switch (body.kind) {
    case 'execute': return <CommandResultBody source={body.source} context={context} />
    case 'edit': return <FileEditDiffBody source={body.diff} view={context?.diffView?.() ?? 'unified'} />
    case 'read': return <ReadFileResultBody source={body.source} context={context} />
    case 'search': return <SearchResultBody source={body.source} context={context} />
    case 'fetch': return <WebFetchResultBody source={body.source} context={context} />
    case 'none': return null
  }
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
  const rawInput = createMemo(() => pickObject(props.toolUse, 'rawInput') ?? undefined)

  const icon = () => kindIcon(kind())
  const command = createMemo(() => pickString(rawInput(), 'command'))
  const description = () => pickString(rawInput(), 'description')
  const fallbackTitle = () => description() || (props.toolUse.title as string | undefined) || kind() || 'Tool'

  // Memo: read 5+ times per render across the JSX below; without this,
  // `extractToolOutput` re-walks `props.toolUse.content` each access.
  const output = createMemo(() => extractToolOutput(props.toolUse))
  const outputText = createMemo(() => stripLeadingBlankLines(output().text))

  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.OPENCODE_TOOL_CALL_UPDATE)
  const { copied: commandCopied, copy: copyCommand } = useCopyButton(() => command())

  // Output collapsing — shared hook keeps isCollapsed/display memoized.
  const collapsed = useCollapsedLines({ text: outputText, expanded })

  // Edit-kind diffs take priority: a tool_call_update with `content[].type=diff`
  // can land on any kind in principle, and we render the diff unconditionally
  // when present.
  const body = createMemo<AcpKindBody>(() => {
    const diff = pickFileEditDiff(
      acpFileEditFromToolCallContent(props.toolUse.content),
      acpFileEditFromToolCallRawInput(kind(), rawInput()),
    )
    if (diff)
      return { kind: 'edit', diff }
    switch (kind()) {
      case ACP_TOOL_KIND.EXECUTE: {
        const source = acpExecuteFromToolCall(props.toolUse)
        return source ? { kind: 'execute', source } : { kind: 'none' }
      }
      case ACP_TOOL_KIND.READ: {
        const source = acpReadFromToolCall(props.toolUse)
        return source && source.lines !== null ? { kind: 'read', source } : { kind: 'none' }
      }
      case ACP_TOOL_KIND.SEARCH: {
        const source = acpSearchFromToolCall(props.toolUse)
        return source ? { kind: 'search', source } : { kind: 'none' }
      }
      case ACP_TOOL_KIND.FETCH: {
        const source = acpWebFetchFromToolCall(props.toolUse)
        return source ? { kind: 'fetch', source } : { kind: 'none' }
      }
    }
    return { kind: 'none' }
  })

  // Kind-specific title
  const title = (): string | JSX.Element => {
    switch (kind()) {
      case ACP_TOOL_KIND.SEARCH: return searchTitle(rawInput(), fallbackTitle(), props.context)
      case ACP_TOOL_KIND.READ: return readTitle(rawInput(), fallbackTitle(), props.context)
      default: return fallbackTitle()
    }
  }

  // Kind-specific summary (only EXECUTE has one today)
  const summary = (): JSX.Element | undefined => {
    if (kind() !== ACP_TOOL_KIND.EXECUTE)
      return undefined
    const cmd = command()
    if (!cmd)
      return undefined
    return <div class={toolInputSummary} innerHTML={renderBashHighlight(firstNonEmptyLine(cmd) ?? cmd)} />
  }

  // Expand/collapse
  const multiLine = createMemo(() => isMultiLineCommand(command()))
  const hasExpandable = () => multiLine() || hasMoreLinesThan(outputText(), COLLAPSED_RESULT_ROWS)
  const expandLabel = () => multiLine() ? 'Show full command' : 'Expand output'

  // Execute-specific: hide summary when expanded + multi-line command
  const displaySummary = () => expanded() && multiLine() ? undefined : summary()

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
        onCopyContent: command() ? copyCommand : undefined,
        contentCopied: commandCopied(),
        copyContentLabel: 'Copy Command',
      }}
      alwaysVisible
    >
      {/* Execute: full multi-line command (when expanded) */}
      <Show when={kind() === ACP_TOOL_KIND.EXECUTE && expanded() && multiLine()}>
        <MultiLineCommandBody command={command()} />
      </Show>

      <Show
        when={body().kind !== 'none'}
        fallback={(
          <Show when={outputText()}>
            <CollapsibleContent kind="ansi-or-pre" text={outputText()} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} />
          </Show>
        )}
      >
        {renderKindBody(body(), props.context)}
      </Show>

      {/* Error indicator: shown only when there's no output text and no
          execute body — execute renders its own status header inside CommandResultBody. */}
      <Show when={output().error && !outputText() && body().kind !== 'edit' && body().kind !== 'execute'}>
        <ToolHeaderRow icon={CircleAlert} title="Failed" />
      </Show>
    </ToolUseLayout>
  )
}

/** Render an ACP tool_call_update (completed/failed) — combined tool_use + tool_result. */
export function acpToolCallUpdateRenderer(toolUse: Record<string, unknown>, _role: MessageRole, context?: RenderContext): JSX.Element {
  return <ToolCallUpdateMessage toolUse={toolUse} context={context} />
}
