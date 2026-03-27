/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { StructuredPatchHunk } from './diffUtils'
import type { RenderContext } from './messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { CommandStreamSegment } from '~/stores/chat.store'
import Bot from 'lucide-solid/icons/bot'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FileEdit from 'lucide-solid/icons/file-pen-line'
import FilePlus from 'lucide-solid/icons/file-plus'
import Globe from 'lucide-solid/icons/globe'
import ListTodo from 'lucide-solid/icons/list-todo'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import Terminal from 'lucide-solid/icons/terminal'
import Wrench from 'lucide-solid/icons/wrench'
import { createEffect, createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { TodoList } from '~/components/todo/TodoList'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { codexPlanToTodos } from '~/lib/messageParser'
import { renderMarkdown, shikiHighlighter } from '~/lib/renderMarkdown'
import { getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { inlineFlex } from '~/styles/shared.css'
import { DiffView, rawDiffToHunks } from './diffUtils'
import { markdownContent } from './markdownContent.css'
import {
  resultDivider,
  thinkingChevron,
  thinkingChevronExpanded,
  thinkingContent,
  thinkingHeader,
} from './messageStyles.css'
import { isObject, relativizePath } from './messageUtils'
import { useSharedExpandedState } from './messageRenderers'
import { formatDuration } from './rendererUtils'
import { renderToolDetail } from './toolDetailRenderers'
import { ToolResultMessage, ToolUseLayout } from './toolRenderers'
import {
  commandStreamContainer,
  commandStreamInteraction,
  toolInputCode,
  toolInputPath,
  toolInputSummary,
  toolInputText,
  toolMessage,
  toolResultCollapsed,
  toolResultContent,
  toolResultContentPre,
  toolResultError,
  toolResultPrompt,
  toolUseIcon,
} from './toolStyles.css'

/** Regex to strip shell wrappers like `/bin/zsh -lc '...'` from commands. */
const SHELL_WRAPPER_RE = /^\/bin\/(?:ba|z)?sh\s+-lc\s+'(.+)'$/
const CODEX_DIFF_HEADER_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/
const DIV_OPEN_RE = /<div\b/g
const DIV_CLOSE_RE = /<\/div>/g
const TOOL_USE_HEADER_CLASS_FRAGMENT = 'toolUseHeader__'

function LiveStreamOutput(props: { stream: () => CommandStreamSegment[] }): JSX.Element {
  return (
    <div class={commandStreamContainer}>
      <For each={props.stream()}>
        {segment => (
          <div class={toolResultContentPre}>{segment.text}</div>
        )}
      </For>
    </div>
  )
}

interface ParsedCodexDiff {
  hunks: StructuredPatchHunk[]
  oldText: string
  newText: string
}

/** Extract the item from Codex native params: {item: {...}, threadId, turnId} */
function extractItem(parsed: unknown): Record<string, unknown> | null {
  if (!isObject(parsed))
    return null
  const item = parsed.item as Record<string, unknown> | undefined
  if (isObject(item))
    return item
  // Sometimes the item IS the top-level object (for item/completed messages stored directly)
  if (parsed.type && typeof parsed.type === 'string')
    return parsed
  return null
}

function firstCommandLine(command: string): string {
  return command.split('\n').find(line => line.trim()) || command
}

function renderBashHighlight(code: string): string {
  return shikiHighlighter.codeToHtml(code, {
    lang: 'bash',
    themes: { light: 'github-light', dark: 'github-dark' },
    defaultColor: false,
  })
}

function parseCodexUnifiedDiff(diff: string): ParsedCodexDiff | null {
  if (!diff.trim())
    return null

  const lines = diff.split('\n')
  const hunks: StructuredPatchHunk[] = []
  const oldLines: string[] = []
  const newLines: string[] = []
  let current: StructuredPatchHunk | null = null

  for (const line of lines) {
    const header = line.match(CODEX_DIFF_HEADER_RE)
    if (header) {
      current = {
        oldStart: Number.parseInt(header[1], 10),
        oldLines: header[2] ? Number.parseInt(header[2], 10) : 1,
        newStart: Number.parseInt(header[3], 10),
        newLines: header[4] ? Number.parseInt(header[4], 10) : 1,
        lines: [],
      }
      hunks.push(current)
      continue
    }
    if (!current)
      continue
    if (line.startsWith('\\ No newline at end of file'))
      continue
    if (!line.startsWith('+') && !line.startsWith('-') && !line.startsWith(' '))
      continue

    current.lines.push(line)
    const prefix = line[0]
    const text = line.slice(1)
    if (prefix === '+' || prefix === ' ')
      newLines.push(text)
    if (prefix === '-' || prefix === ' ')
      oldLines.push(text)
  }

  if (hunks.length === 0)
    return null

  return {
    hunks,
    oldText: oldLines.join('\n'),
    newText: newLines.join('\n'),
  }
}

function codexChangeKind(change: Record<string, unknown>): string {
  const kind = change.kind
  if (typeof kind === 'string')
    return kind
  if (isObject(kind) && typeof kind.type === 'string')
    return kind.type as string
  return ''
}

function isSimpleEditChange(change: Record<string, unknown>): boolean {
  return codexChangeKind(change) === 'update' && typeof change.diff === 'string' && !!parseCodexUnifiedDiff(change.diff as string)
}

function isSimpleAddChange(change: Record<string, unknown>): boolean {
  return codexChangeKind(change) === 'add' && typeof change.diff === 'string' && (change.diff as string).length > 0
}

function completedFileChangeEntries(changes: Array<Record<string, unknown>>): Array<
  | { kind: 'diff', path: string, hunks: StructuredPatchHunk[] }
  | { kind: 'add', path: string, hunks: StructuredPatchHunk[] }
> {
  return changes.flatMap((change) => {
    const path = typeof change.path === 'string' ? change.path : ''
    const diffText = typeof change.diff === 'string' ? change.diff : ''
    const parsed = parseCodexUnifiedDiff(diffText)
    if (parsed) {
      return [{ kind: 'diff' as const, path, hunks: parsed.hunks }]
    }
    if (isSimpleAddChange(change)) {
      return [{ kind: 'add' as const, path, hunks: rawDiffToHunks('', diffText) }]
    }
    return []
  })
}

function diffStatsFromHunks(hunks: StructuredPatchHunk[]): { added: number, deleted: number } {
  let added = 0
  let deleted = 0
  for (const hunk of hunks) {
    for (const line of hunk.lines) {
      if (line.startsWith('+'))
        added++
      else if (line.startsWith('-'))
        deleted++
    }
  }
  return { added, deleted }
}

function stripToolUseHeaderFromOutput(output: string): string {
  if (!output.includes(TOOL_USE_HEADER_CLASS_FRAGMENT))
    return output

  const lines = output.split('\n')
  const kept: string[] = []

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (!line.includes(TOOL_USE_HEADER_CLASS_FRAGMENT)) {
      kept.push(line)
      continue
    }

    if (kept.length > 0 && kept.at(-1).includes('<div'))
      kept.pop()

    let depth = 1
    while (++i < lines.length) {
      const current = lines[i]
      depth += (current.match(DIV_OPEN_RE) || []).length
      depth -= (current.match(DIV_CLOSE_RE) || []).length
      if (depth <= 0)
        break
    }
  }

  return kept.join('\n')
}

function extractPlanParams(parsed: unknown): Record<string, unknown> | null {
  if (!isObject(parsed))
    return null
  if (parsed.method === 'turn/plan/updated' && isObject(parsed.params))
    return parsed.params as Record<string, unknown>
  if (Array.isArray(parsed.plan))
    return parsed
  return null
}

function codexWebSearchAction(parsed: unknown): Record<string, unknown> | null {
  if (!isObject(parsed))
    return null
  return isObject(parsed.action) ? parsed.action as Record<string, unknown> : null
}

function codexWebSearchActionType(action: Record<string, unknown> | null): string {
  return typeof action?.type === 'string' ? action.type as string : ''
}

function codexWebSearchQueries(action: Record<string, unknown> | null): string[] {
  if (codexWebSearchActionType(action) !== 'search')
    return []
  const direct = typeof action?.query === 'string' ? action.query.trim() : ''
  const listed = Array.isArray(action?.queries)
    ? action.queries.filter(q => typeof q === 'string').map(q => (q as string).trim()).filter(Boolean)
    : []
  const merged = direct ? [direct, ...listed] : listed
  return merged.filter((query, index) => merged.indexOf(query) === index)
}

function codexWebSearchActionDetail(action: Record<string, unknown> | null, query: string): string {
  const actionType = codexWebSearchActionType(action)
  if (actionType === 'search') {
    const queries = codexWebSearchQueries(action)
    if (queries.length > 0)
      return queries[0]
    return query
  }
  if (actionType === 'openPage')
    return typeof action?.url === 'string' ? action.url : query
  if (actionType === 'findInPage') {
    const url = typeof action?.url === 'string' ? action.url : ''
    const pattern = typeof action?.pattern === 'string' ? action.pattern : ''
    if (pattern && url)
      return `'${pattern}' in ${url}`
    if (pattern)
      return `'${pattern}'`
    if (url)
      return url
  }
  return query
}

function renderCodexWebSearchTitle(action: Record<string, unknown> | null, detail: string, context?: RenderContext): JSX.Element | string {
  const actionType = codexWebSearchActionType(action)
  if (actionType === 'openPage') {
    return renderToolDetail('WebFetch', { url: detail }, context) || detail || 'Open page'
  }
  if (actionType === 'search') {
    return renderToolDetail('WebSearch', { query: detail }, context) || detail || 'Web search'
  }
  if (actionType === 'findInPage') {
    const url = typeof action?.url === 'string' ? action.url : ''
    const pattern = typeof action?.pattern === 'string' ? action.pattern : ''
    if (pattern && url) {
      return (
        <>
          <span class={toolInputCode}>{`"${pattern}"`}</span>
          <span class={toolInputText}>{' in '}</span>
          {renderToolDetail('WebFetch', { url }, context) || <span class={toolInputText}>{url}</span>}
        </>
      )
    }
    if (pattern)
      return <span class={toolInputCode}>{`"${pattern}"`}</span>
    if (url)
      return renderToolDetail('WebFetch', { url }, context) || url
  }
  return detail || 'Searching the web'
}

/** Renders Codex agentMessage items as markdown. */
export function codexAgentMessageRenderer(parsed: unknown, _role: MessageRole, _context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'agentMessage')
    return null
  const text = (item.text as string) || ''
  if (!text)
    return null
  return <div class={markdownContent} innerHTML={renderMarkdown(text)} />
}

/** Renders Codex plan items using ToolUseLayout without a bubble (same pattern as ExitPlanMode). */
export function codexPlanRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'plan')
    return null
  const text = (item.text as string) || ''
  if (!text)
    return null
  return (
    <ToolUseLayout
      icon={PlaneTakeoff}
      toolName="Plan"
      title="Proposed Plan"
      alwaysVisible={true}
      bordered={false}
      context={context}
    >
      <hr />
      <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={renderMarkdown(text)} />
    </ToolUseLayout>
  )
}

/** Renders Codex turn/plan/updated notifications with the same todo-list UI pattern as TodoWrite. */
export function codexTurnPlanRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const params = extractPlanParams(parsed)
  if (!params)
    return null

  const plan = params.plan
  if (!Array.isArray(plan))
    return null
  const todos = codexPlanToTodos(plan)
  if (todos.length === 0)
    return null

  const explanation = typeof params.explanation === 'string' ? params.explanation.trim() : ''
  const label = `${todos.length} task${todos.length === 1 ? '' : 's'}${explanation ? ` - ${explanation}` : ''}`

  return (
    <ToolUseLayout
      icon={ListTodo}
      toolName="Plan Update"
      title={label}
      alwaysVisible={true}
      context={context}
    >
      <TodoList todos={todos} />
    </ToolUseLayout>
  )
}

/** Renders Codex webSearch items using WebSearch/WebFetch-style tool cards. */
export function codexWebSearchRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'webSearch')
    return null

  const query = typeof item.query === 'string' ? item.query : ''
  const action = codexWebSearchAction(item)
  const actionType = codexWebSearchActionType(action)
  const detail = codexWebSearchActionDetail(action, query)
  const queries = codexWebSearchQueries(action)
  const isStartMessage = actionType === 'other' && !query.trim()
  const [expanded, setExpanded] = useSharedExpandedState(context, 'codex-web-search')

  if (isStartMessage) {
    return (
      <ToolUseLayout
        icon={Globe}
        toolName="WebSearch"
        title="Searching the web"
        alwaysVisible={true}
        context={context}
      />
    )
  }

  if (!detail.trim())
    return null

  return (
    <ToolUseLayout
      icon={Globe}
      toolName={actionType === 'openPage' ? 'WebFetch' : 'WebSearch'}
      title={renderCodexWebSearchTitle(action, detail, context)}
      context={context}
      expanded={expanded()}
      onToggleExpand={queries.length > 1 ? () => setExpanded(v => !v) : undefined}
      alwaysVisible={queries.length <= 1}
    >
      <Show when={queries.length > 1}>
        <For each={queries.slice(1)}>
          {extraQuery => (
            <div class={toolInputSummary}>
              {renderToolDetail('WebSearch', { query: extraQuery }, context) || extraQuery}
            </div>
          )}
        </For>
      </Show>
    </ToolUseLayout>
  )
}

/** Renders Codex commandExecution items using shared ToolUseLayout. */
export function codexCommandExecutionRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'commandExecution')
    return null

  const rawCommand = (item.command as string) || '(command)'
  // Strip shell wrappers like `/bin/zsh -lc '...'` to show the actual command.
  const command = rawCommand.replace(SHELL_WRAPPER_RE, '$1')
  const cwd = (item.cwd as string) || ''
  const status = (item.status as string) || ''
  const output = stripToolUseHeaderFromOutput((item.aggregatedOutput as string) || '')
  const exitCode = item.exitCode as number | null | undefined
  const durationMs = item.durationMs as number | null | undefined
  const isTerminal = status === 'completed' || status === 'failed'
  const hasError = status === 'failed' || (isTerminal && exitCode != null && exitCode !== 0)
  const liveStream = () => context?.commandStream ?? []
  const hasLiveStream = () => liveStream().length > 0
  const [expanded, setExpanded] = useSharedExpandedState(context, 'codex-command-execution')
  createEffect(() => {
    if (hasLiveStream())
      setExpanded(true)
  })
  const displayCommand = firstCommandLine(command)
  const title = renderToolDetail('Bash', { description: 'Run command', command }, context) || 'Run command'

  const statusParts = (): string => {
    const parts: string[] = []
    if (exitCode != null)
      parts.push(`exit ${exitCode}`)
    if (durationMs != null)
      parts.push(formatDuration(durationMs))
    return parts.join(' · ')
  }
  const resultStatusDetail = (): string => {
    const parts: string[] = []
    if (exitCode != null && exitCode !== 0)
      parts.push(`exit code: ${exitCode}`)
    return parts.join(' · ')
  }

  if (isTerminal) {
    return (
      <ToolResultMessage
        toolName="Bash"
        resultContent={output}
        isPreText={true}
        structuredPatch={null}
        oldStr=""
        newStr=""
        filePath=""
        isError={hasError}
        statusDetail={resultStatusDetail()}
        context={context}
      />
    )
  }

  return (
    <ToolUseLayout
      icon={Terminal}
      toolName="Command Execution"
      title={title}
      summary={(
        <>
          <div class={toolInputSummary} innerHTML={renderBashHighlight(displayCommand)} />
          <Show when={statusParts()}>
            <div class={toolInputSummary}>{statusParts()}</div>
          </Show>
        </>
      )}
      context={context}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
    >
      <Show when={cwd}>
        <div class={toolInputSummary}>
          cwd:
          {' '}
          {relativizePath(cwd, context?.workingDir, context?.homeDir)}
        </div>
      </Show>
      <Show
        when={hasLiveStream()}
        fallback={<Show when={output}><div class={toolResultContentPre}>{output}</div></Show>}
      >
        <div class={commandStreamContainer}>
          <For each={liveStream()}>
            {segment => (
              <div class={segment.kind === 'interaction' ? commandStreamInteraction : toolResultContentPre}>
                {segment.kind === 'interaction' ? `> ${segment.text}` : segment.text}
              </div>
            )}
          </For>
        </div>
      </Show>
    </ToolUseLayout>
  )
}

/** Renders Codex fileChange items using shared ToolUseLayout. */
export function codexFileChangeRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'fileChange')
    return null

  const changes = (item.changes as Array<Record<string, unknown>>) || []
  const status = (item.status as string) || ''
  const liveStream = () => context?.commandStream ?? []
  const hasLiveStream = () => liveStream().length > 0
  const completedEntries = completedFileChangeEntries(changes)
  const simpleAdd = changes.length === 1 && isSimpleAddChange(changes[0]) ? changes[0] : null
  const simpleAddPath = simpleAdd ? ((simpleAdd.path as string) || '') : ''
  const simpleAddContent = simpleAdd ? ((simpleAdd.diff as string) || '') : ''

  if (status === 'completed' && simpleAdd) {
    return (
      <ToolResultMessage
        toolName="Write"
        resultContent=""
        isPreText={false}
        structuredPatch={rawDiffToHunks('', simpleAddContent)}
        oldStr=""
        newStr={simpleAddContent}
        filePath={simpleAddPath}
        context={context}
      />
    )
  }

  if (status === 'completed' && completedEntries.length > 0) {
    const showPerFileLabels = completedEntries.length > 1
    return (
      <div class={toolMessage}>
        <For each={completedEntries}>
          {(entry) => {
            const stats = diffStatsFromHunks(entry.hunks)
            return (
              <div>
                <Show when={showPerFileLabels}>
                  <div class={toolResultPrompt}>
                    <span class={toolInputPath}>{relativizePath(entry.path, context?.workingDir, context?.homeDir)}</span>
                    {' '}
                    <DiffStatsBadge added={stats.added} deleted={stats.deleted} class={toolInputText} />
                  </div>
                </Show>
                <DiffView
                  hunks={entry.hunks}
                  view={context?.diffView ?? 'unified'}
                  filePath={entry.path}
                />
              </div>
            )
          }}
        </For>
      </div>
    )
  }

  if (status === 'completed') {
    return (
      <div class={toolMessage}>
        <Show when={changes.length > 0 && completedEntries.length === 0}>
          <div class={toolResultPrompt}>
            {changes.length === 1 ? '1 file changed' : `${changes.length} files changed`}
          </div>
        </Show>
      </div>
    )
  }

  const simpleEdit = changes.length === 1 && isSimpleEditChange(changes[0]) ? changes[0] : null
  const parsedDiff = simpleEdit ? parseCodexUnifiedDiff(simpleEdit.diff as string) : null
  const inProgressDetail = simpleAdd
    ? { icon: FilePlus, title: renderToolDetail('Write', { file_path: simpleAddPath, content: simpleAddContent }, context), path: simpleAddPath }
    : simpleEdit && parsedDiff
      ? { icon: FileEdit, title: renderToolDetail('Edit', { file_path: (simpleEdit.path as string) || '', old_string: parsedDiff.oldText, new_string: parsedDiff.newText }, context), path: (simpleEdit.path as string) || '' }
      : null

  if (inProgressDetail) {
    const title = inProgressDetail.title || (
      <span class={toolInputPath}>{relativizePath(inProgressDetail.path, context?.workingDir, context?.homeDir)}</span>
    )

    return (
      <ToolUseLayout
        icon={inProgressDetail.icon}
        toolName="File Change"
        title={title}
        context={context}
        alwaysVisible={true}
      >
        <Show when={hasLiveStream()}>
          <LiveStreamOutput stream={liveStream} />
        </Show>
      </ToolUseLayout>
    )
  }

  const titleEl = (
    <span class={toolInputSummary}>
      {changes.length === 1
        ? relativizePath(String(changes[0].path || 'file'), context?.workingDir, context?.homeDir)
        : `${changes.length} files`}
    </span>
  )

  return (
    <ToolUseLayout
      icon={FileEdit}
      toolName="File Change"
      title={titleEl}
      context={context}
      alwaysVisible={true}
    >
      <Show when={hasLiveStream()}>
        <LiveStreamOutput stream={liveStream} />
      </Show>
      <For each={changes.length === 1 ? [] : changes}>
        {(change) => {
          const path = relativizePath((change.path as string) || '(unknown)', context?.workingDir, context?.homeDir)
          const kind = codexChangeKind(change)
          return (
            <div class={toolInputPath}>
              {path}
              {' '}
              <span class={toolInputSummary}>
                (
                {kind}
                )
              </span>
            </div>
          )
        }}
      </For>
    </ToolUseLayout>
  )
}

/** Renders Codex reasoning items with expandable content. */
export function codexReasoningRenderer(parsed: unknown, _role: MessageRole, _context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'reasoning')
    return null

  const summary = (item.summary as string[]) || []
  const content = (item.content as string[]) || []
  const liveStream = () => _context?.commandStream ?? []
  const liveSummary = () => {
    const parts: string[] = []
    let current = ''
    for (const segment of liveStream()) {
      if (segment.kind === 'reasoning_summary_break') {
        if (current) {
          parts.push(current)
          current = ''
        }
        continue
      }
      if (segment.kind === 'reasoning_summary')
        current += segment.text
    }
    if (current)
      parts.push(current)
    return parts
  }
  const liveContent = () => liveStream()
    .filter(segment => segment.kind === 'reasoning_content')
    .map(segment => segment.text)
  const text = () => {
    const streamedSummary = liveSummary()
    if (streamedSummary.length > 0)
      return streamedSummary.join('\n\n')
    const streamedContent = liveContent()
    if (streamedContent.length > 0)
      return streamedContent.join('\n')
    return summary.join('\n') || content.join('\n') || ''
  }
  if (!text())
    return null

  const [expanded, setExpanded] = useSharedExpandedState(_context, 'codex-reasoning')

  return (
    <div>
      <div class={thinkingHeader} onClick={() => setExpanded(v => !v)}>
        <Tooltip text="Reasoning">
          <span class={`${inlineFlex} ${toolUseIcon}`}>
            <Icon icon={Brain} size="md" />
          </span>
        </Tooltip>
        <span class={toolInputSummary}>Thinking</span>
        <span class={`${inlineFlex} ${thinkingChevron}${expanded() ? ` ${thinkingChevronExpanded}` : ''}`}>
          <Icon icon={ChevronRight} size="sm" class={toolUseIcon} />
        </span>
      </div>
      <Show when={expanded()}>
        <div class={thinkingContent}>
          <div class={markdownContent} innerHTML={renderMarkdown(text())} />
        </div>
      </Show>
    </div>
  )
}

/** Build a title element with a display name and optional status badge. */
function codexStatusTitle(displayName: string, status: string): JSX.Element {
  return (
    <>
      <span class={toolInputSummary}>{displayName}</span>
      <Show when={status}>
        <span class={toolInputSummary}>{status}</span>
      </Show>
    </>
  )
}

/** Renders Codex mcpToolCall items using shared ToolUseLayout. */
export function codexMcpToolCallRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || (item.type !== 'mcpToolCall' && item.type !== 'dynamicToolCall'))
    return null

  const server = (item.server as string) || ''
  const tool = (item.tool as string) || 'Tool'
  const status = (item.status as string) || ''
  const args = item.arguments ? JSON.stringify(item.arguments, null, 2) : ''
  const result = item.result as Record<string, unknown> | undefined
  const error = item.error as Record<string, unknown> | undefined
  const [expanded, setExpanded] = useSharedExpandedState(context, 'codex-mcp-tool-call')

  const titleEl = codexStatusTitle(server ? `${server}/${tool}` : tool, status)

  if (status === 'completed' || status === 'failed') {
    return (
      <div class={toolMessage}>
        <Show when={result}>
          <div>
            <div class={toolResultPrompt}>Result</div>
            <div class={toolResultContentPre}>{JSON.stringify(result, null, 2)}</div>
          </div>
        </Show>
        <Show when={error}>
          <div>
            <div class={toolResultPrompt}>Error</div>
            <div class={toolResultError}>{JSON.stringify(error, null, 2)}</div>
          </div>
        </Show>
        <Show when={!result && !error && status}>
          <div class={toolResultPrompt}>{status}</div>
        </Show>
      </div>
    )
  }

  return (
    <ToolUseLayout
      icon={Wrench}
      toolName="MCP Tool Call"
      title={titleEl}
      context={context}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
    >
      <Show when={args}>
        <div>
          <div class={toolInputSummary}>Arguments:</div>
          <div class={toolResultContentPre}>{args}</div>
        </div>
      </Show>
      <Show when={result}>
        <div>
          <div class={toolInputSummary}>Result:</div>
          <div class={toolResultContentPre}>{JSON.stringify(result, null, 2)}</div>
        </div>
      </Show>
      <Show when={error}>
        <div>
          <div class={toolResultError}>Error:</div>
          <div class={toolResultError}>{JSON.stringify(error, null, 2)}</div>
        </div>
      </Show>
    </ToolUseLayout>
  )
}

/** Renders Codex collabAgentToolCall items (SpawnAgent) using shared ToolUseLayout. */
export function codexCollabAgentToolCallRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'collabAgentToolCall')
    return null

  const tool = (item.tool as string) || 'SpawnAgent'
  const status = (item.status as string) || ''
  const prompt = typeof item.prompt === 'string' ? item.prompt : ''
  const model = typeof item.model === 'string' ? item.model : ''
  const reasoningEffort = typeof item.reasoningEffort === 'string' ? item.reasoningEffort : ''
  const displayName = tool === 'spawnAgent'
    ? 'SpawnAgent'
    : tool === 'wait'
      ? 'Wait'
      : tool
  const isTerminalWait = tool === 'wait' && status !== 'inProgress' && status !== ''
  const isWaitInProgress = tool === 'wait' && !isTerminalWait
  const isSpawnAgent = tool === 'spawnAgent'
  const hasPrompt = prompt.trim() !== ''
  const hasCollapsiblePrompt = (isWaitInProgress || isSpawnAgent) && hasPrompt
  const [expanded, setExpanded] = useSharedExpandedState(context, 'codex-collab-agent-tool-call')
  const modelLabel = model ? (getCachedSettingsLabel('model', model) || model) : ''
  const effortLabel = reasoningEffort ? (getCachedSettingsLabel('effort', reasoningEffort) || reasoningEffort) : ''
  const spawnAgentDetails = [
    modelLabel ? `model: ${modelLabel}` : '',
    effortLabel ? `reasoning effort: ${effortLabel}` : '',
  ].filter(Boolean).join(' · ')
  const titleEl = isTerminalWait
    ? `Wait ${status}`
    : isWaitInProgress
      ? 'Waiting for subagent'
      : isSpawnAgent
        ? (spawnAgentDetails ? `Subagent (${spawnAgentDetails})` : 'Subagent')
        : renderToolDetail('Agent', { description: displayName }, context) || codexStatusTitle(displayName, status)
  const summary = hasCollapsiblePrompt
    ? (
        <div
          class={`${toolResultContent} ${toolResultCollapsed} ${markdownContent}`}
          innerHTML={renderMarkdown(prompt)}
        />
      )
    : isWaitInProgress || isTerminalWait || isSpawnAgent || !status
      ? undefined
      : <div class={toolInputSummary}>{status}</div>

  return (
    <ToolUseLayout
      icon={Bot}
      toolName={displayName}
      title={titleEl}
      summary={expanded() ? undefined : summary}
      context={context}
      expanded={expanded()}
      onToggleExpand={hasCollapsiblePrompt ? () => setExpanded(v => !v) : undefined}
    >
      <Show when={hasCollapsiblePrompt}>
        <div class={`${toolResultContent} ${markdownContent}`} innerHTML={renderMarkdown(prompt)} />
      </Show>
    </ToolUseLayout>
  )
}

/** Renders Codex turn/completed as a result divider. */
export function codexTurnCompletedRenderer(parsed: unknown, _role: MessageRole, _context?: RenderContext): JSX.Element | null {
  if (!isObject(parsed) || !isObject(parsed.turn))
    return null
  const turn = parsed.turn as Record<string, unknown>
  const status = (turn.status as string) || ''
  if (!status)
    return null

  // Failed turn: show error message from turn.error.message
  if (status === 'failed' && isObject(turn.error)) {
    const error = turn.error as Record<string, unknown>
    const message = typeof error.message === 'string' ? error.message : 'Unknown error'
    const details = typeof error.additionalDetails === 'string' ? error.additionalDetails : ''
    const label = details ? `${message} — ${details}` : message
    return <div class={resultDivider} style={{ color: 'var(--danger)' }}>{label}</div>
  }

  return <div class={resultDivider}>{`Turn ${status}`}</div>
}
