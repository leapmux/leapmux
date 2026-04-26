/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Bot from 'lucide-solid/icons/bot'
import { createMemo, Show } from 'solid-js'
import { pickString } from '~/lib/jsonPick'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { markdownContent } from '../../../markdownEditor/markdownContent.css'
import { useSharedExpandedState } from '../../../messageRenderers'
import { ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary, toolResultCollapsed, toolResultContent } from '../../../toolStyles.css'
import { renderAgentTitle } from '../../../toolTitleRenderers'
import { extractItem } from '../renderHelpers'
import { codexStatusTitle } from './statusTitle'

/** Renders Codex collabAgentToolCall items (SpawnAgent) using shared ToolUseLayout. */
export function codexCollabAgentToolCallRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'collabAgentToolCall')
    return null

  const tool = (item.tool as string) || 'SpawnAgent'
  const status = (item.status as string) || ''
  const prompt = pickString(item, 'prompt')
  const model = pickString(item, 'model')
  const reasoningEffort = pickString(item, 'reasoningEffort')
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
  const [expanded, setExpanded] = useSharedExpandedState(() => context, 'codex-collab-agent-tool-call')
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
        : renderAgentTitle(displayName) || codexStatusTitle(displayName, status)
  const promptHtml = createMemo(() => hasCollapsiblePrompt ? renderMarkdown(prompt) : '')
  const summary = hasCollapsiblePrompt
    ? (
        <div
          class={`${toolResultContent} ${toolResultCollapsed} ${markdownContent}`}
          innerHTML={promptHtml()}
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
        <div class={`${toolResultContent} ${markdownContent}`} innerHTML={promptHtml()} />
      </Show>
    </ToolUseLayout>
  )
}
