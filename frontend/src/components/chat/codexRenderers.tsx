/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Bot from 'lucide-solid/icons/bot'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FileEdit from 'lucide-solid/icons/file-pen-line'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import SquareTerminal from 'lucide-solid/icons/square-terminal'
import Wrench from 'lucide-solid/icons/wrench'
import { createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownContent.css'
import {
  resultDivider,
  thinkingChevron,
  thinkingChevronExpanded,
  thinkingContent,
  thinkingHeader,
} from './messageStyles.css'
import { isObject } from './messageUtils'
import { ToolUseLayout } from './toolRenderers'
import {
  toolInputPath,
  toolInputSummary,
  toolResultContentPre,
  toolResultError,
  toolUseIcon,
} from './toolStyles.css'

/** Regex to strip shell wrappers like `/bin/zsh -lc '...'` from commands. */
const SHELL_WRAPPER_RE = /^\/bin\/(?:ba|z)?sh\s+-lc\s+'(.+)'$/

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
      <>
        <hr />
        <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={renderMarkdown(text)} />
      </>
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
  const output = (item.aggregatedOutput as string) || ''
  const exitCode = item.exitCode as number | null | undefined
  const durationMs = item.durationMs as number | null | undefined
  const isCompleted = status === 'completed'
  const hasError = isCompleted && exitCode != null && exitCode !== 0
  const [expanded, setExpanded] = createSignal(hasError || !isCompleted)

  const statusParts = (): string => {
    const parts: string[] = []
    if (!isCompleted && status)
      parts.push(status)
    if (exitCode != null)
      parts.push(`exit ${exitCode}`)
    if (durationMs != null)
      parts.push(`${(durationMs / 1000).toFixed(1)}s`)
    return parts.join(' · ')
  }

  return (
    <ToolUseLayout
      icon={SquareTerminal}
      toolName="Command Execution"
      title={command}
      summary={statusParts() ? <div class={toolInputSummary}>{statusParts()}</div> : undefined}
      context={context}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
    >
      <Show when={cwd}>
        <div class={toolInputSummary}>
          cwd:
          {' '}
          {cwd}
        </div>
      </Show>
      <Show when={output}>
        <div class={toolResultContentPre}>{output}</div>
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
  const [expanded, setExpanded] = createSignal(true)

  const titleEl = (
    <>
      <span class={toolInputSummary}>
        {changes.length === 1 ? String(changes[0].path || 'file') : `${changes.length} files`}
      </span>
      <Show when={status}>
        <span class={toolInputSummary}>{status}</span>
      </Show>
    </>
  )

  return (
    <ToolUseLayout
      icon={FileEdit}
      toolName="File Change"
      title={titleEl}
      context={context}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
    >
      <For each={changes}>
        {(change) => {
          const path = (change.path as string) || '(unknown)'
          const kind = (change.kind as string) || ''
          const diff = (change.diff as string) || ''
          return (
            <div>
              <div class={toolInputPath}>
                {path}
                {' '}
                <span class={toolInputSummary}>
                  (
                  {kind}
                  )
                </span>
              </div>
              <Show when={diff}>
                <div class={toolResultContentPre}>{diff}</div>
              </Show>
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
  const text = summary.join('\n') || content.join('\n') || ''
  if (!text)
    return null

  const [expanded, setExpanded] = createSignal(false)

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
          <div class={markdownContent} innerHTML={renderMarkdown(text)} />
        </div>
      </Show>
    </div>
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
  const [expanded, setExpanded] = createSignal(false)

  const titleEl = (
    <>
      <span class={toolInputSummary}>{server ? `${server}/${tool}` : tool}</span>
      <Show when={status}>
        <span class={toolInputSummary}>{status}</span>
      </Show>
    </>
  )

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
  const displayName = tool === 'spawnAgent' ? 'SpawnAgent' : tool

  const titleEl = (
    <>
      <span class={toolInputSummary}>{displayName}</span>
      <Show when={status}>
        <span class={toolInputSummary}>{status}</span>
      </Show>
    </>
  )

  return (
    <ToolUseLayout
      icon={Bot}
      toolName={displayName}
      title={titleEl}
      context={context}
    />
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
