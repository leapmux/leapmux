/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FileEdit from 'lucide-solid/icons/file-pen-line'
import SquareTerminal from 'lucide-solid/icons/square-terminal'
import Wrench from 'lucide-solid/icons/wrench'
import { createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from '../markdownContent.css'
import {
  thinkingChevron,
  thinkingChevronExpanded,
  thinkingContent,
  thinkingHeader,
} from '../messageStyles.css'
import { isObject } from '../messageUtils'
import {
  toolInputText,
  toolMessage,
  toolUseHeader,
  toolUseIcon,
} from '../toolStyles.css'

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

/** Renders Codex commandExecution items. */
export function codexCommandExecutionRenderer(parsed: unknown, _role: MessageRole, _context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'commandExecution')
    return null

  const command = (item.command as string) || '(command)'
  const cwd = (item.cwd as string) || ''
  const status = (item.status as string) || ''
  const output = (item.aggregatedOutput as string) || ''
  const exitCode = item.exitCode as number | null | undefined
  const durationMs = item.durationMs as number | null | undefined
  const [expanded, setExpanded] = createSignal(status !== 'completed' || (exitCode != null && exitCode !== 0))

  return (
    <div class={toolMessage}>
      <div class={toolUseHeader} onClick={() => setExpanded(v => !v)}>
        <Tooltip text="Command Execution">
          <span class={inlineFlex}>
            <Icon icon={SquareTerminal} size="md" class={toolUseIcon} />
          </span>
        </Tooltip>
        <span class={toolInputText}>{command}</span>
        <Show when={exitCode != null}>
          <span style={{ 'opacity': 0.6, 'font-size': '0.85em', 'margin-left': '8px' }}>
            exit
            {' '}
            {exitCode}
            {durationMs != null ? ` (${(durationMs / 1000).toFixed(1)}s)` : ''}
          </span>
        </Show>
        <span class={`${inlineFlex} ${thinkingChevron}${expanded() ? ` ${thinkingChevronExpanded}` : ''}`}>
          <Icon icon={ChevronRight} size="sm" class={toolUseIcon} />
        </span>
      </div>
      <Show when={expanded()}>
        <div class={thinkingContent}>
          <Show when={cwd}>
            <div style={{ 'opacity': 0.6, 'font-size': '0.85em', 'margin-bottom': '4px' }}>
              cwd:
              {cwd}
            </div>
          </Show>
          <Show when={output}>
            <pre style={{ 'white-space': 'pre-wrap', 'word-break': 'break-all', 'max-height': '400px', 'overflow': 'auto' }}>{output}</pre>
          </Show>
        </div>
      </Show>
    </div>
  )
}

/** Renders Codex fileChange items. */
export function codexFileChangeRenderer(parsed: unknown, _role: MessageRole, _context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'fileChange')
    return null

  const changes = (item.changes as Array<Record<string, unknown>>) || []
  const status = (item.status as string) || ''
  const [expanded, setExpanded] = createSignal(true)

  return (
    <div class={toolMessage}>
      <div class={toolUseHeader} onClick={() => setExpanded(v => !v)}>
        <Tooltip text="File Change">
          <span class={inlineFlex}>
            <Icon icon={FileEdit} size="md" class={toolUseIcon} />
          </span>
        </Tooltip>
        <span class={toolInputText}>
          {changes.length === 1 ? String(changes[0].path || 'file') : `${changes.length} files`}
        </span>
        <Show when={status}>
          <span style={{ 'opacity': 0.6, 'font-size': '0.85em', 'margin-left': '8px' }}>{status}</span>
        </Show>
        <span class={`${inlineFlex} ${thinkingChevron}${expanded() ? ` ${thinkingChevronExpanded}` : ''}`}>
          <Icon icon={ChevronRight} size="sm" class={toolUseIcon} />
        </span>
      </div>
      <Show when={expanded()}>
        <div class={thinkingContent}>
          <For each={changes}>
            {(change) => {
              const path = (change.path as string) || '(unknown)'
              const kind = (change.kind as string) || ''
              const diff = (change.diff as string) || ''
              return (
                <div style={{ 'margin-bottom': '8px' }}>
                  <div style={{ 'font-weight': 'bold', 'margin-bottom': '4px' }}>
                    {path}
                    {' '}
                    <span style={{ opacity: 0.6 }}>
                      (
                      {kind}
                      )
                    </span>
                  </div>
                  <Show when={diff}>
                    <pre style={{ 'white-space': 'pre-wrap', 'word-break': 'break-all', 'max-height': '400px', 'overflow': 'auto' }}>{diff}</pre>
                  </Show>
                </div>
              )
            }}
          </For>
        </div>
      </Show>
    </div>
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
    <div class={toolMessage}>
      <div class={thinkingHeader} onClick={() => setExpanded(v => !v)}>
        <Tooltip text="Reasoning">
          <span class={inlineFlex}>
            <Icon icon={Brain} size="md" class={toolUseIcon} />
          </span>
        </Tooltip>
        <span class={toolInputText}>Thinking</span>
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

/** Renders Codex mcpToolCall items. */
export function codexMcpToolCallRenderer(parsed: unknown, _role: MessageRole, _context?: RenderContext): JSX.Element | null {
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

  return (
    <div class={toolMessage}>
      <div class={toolUseHeader} onClick={() => setExpanded(v => !v)}>
        <Tooltip text="MCP Tool Call">
          <span class={inlineFlex}>
            <Icon icon={Wrench} size="md" class={toolUseIcon} />
          </span>
        </Tooltip>
        <span class={toolInputText}>{server ? `${server}/${tool}` : tool}</span>
        <Show when={status}>
          <span style={{ 'opacity': 0.6, 'font-size': '0.85em', 'margin-left': '8px' }}>{status}</span>
        </Show>
        <span class={`${inlineFlex} ${thinkingChevron}${expanded() ? ` ${thinkingChevronExpanded}` : ''}`}>
          <Icon icon={ChevronRight} size="sm" class={toolUseIcon} />
        </span>
      </div>
      <Show when={expanded()}>
        <div class={thinkingContent}>
          <Show when={args}>
            <div style={{ 'margin-bottom': '8px' }}>
              <div style={{ 'opacity': 0.6, 'margin-bottom': '4px' }}>Arguments:</div>
              <pre style={{ 'white-space': 'pre-wrap', 'max-height': '200px', 'overflow': 'auto' }}>{args}</pre>
            </div>
          </Show>
          <Show when={result}>
            <div>
              <div style={{ 'opacity': 0.6, 'margin-bottom': '4px' }}>Result:</div>
              <pre style={{ 'white-space': 'pre-wrap', 'max-height': '200px', 'overflow': 'auto' }}>{JSON.stringify(result, null, 2)}</pre>
            </div>
          </Show>
          <Show when={error}>
            <div style={{ color: 'var(--danger)' }}>
              <div style={{ 'opacity': 0.6, 'margin-bottom': '4px' }}>Error:</div>
              <pre>{JSON.stringify(error, null, 2)}</pre>
            </div>
          </Show>
        </div>
      </Show>
    </div>
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
  return (
    <div style={{ 'text-align': 'center', 'opacity': 0.5, 'font-size': '0.85em', 'padding': '4px 0' }}>
      Turn
      {' '}
      {status}
    </div>
  )
}
