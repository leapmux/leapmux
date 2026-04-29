/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import Bot from 'lucide-solid/icons/bot'
import { createMemo, Show } from 'solid-js'
import { pickString } from '~/lib/jsonPick'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { markdownContent } from '../../../markdownEditor/markdownContent.css'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { joinMetaParts } from '../../../rendererUtils'
import { ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary, toolResultCollapsed, toolResultContent } from '../../../toolStyles.css'
import { renderAgentTitle } from '../../../toolTitleRenderers'
import { defineCodexRenderer } from '../defineRenderer'
import { codexStatusTitle } from './statusTitle'

// Registry-only: dispatched by `item.type === 'collabAgentToolCall'` via
// `CODEX_RENDERERS` (loaded from `renderers/registerAll.ts`).
defineCodexRenderer({
  itemTypes: [CODEX_ITEM.COLLAB_AGENT_TOOL_CALL],
  render: (props) => {
    const tool = (): string => (props.item.tool as string) || 'SpawnAgent'
    const status = (): string => (props.item.status as string) || ''
    const prompt = (): string => pickString(props.item, 'prompt')
    const model = (): string => pickString(props.item, 'model')
    const reasoningEffort = (): string => pickString(props.item, 'reasoningEffort')
    const displayName = createMemo(() => {
      const t = tool()
      if (t === 'spawnAgent')
        return 'SpawnAgent'
      if (t === 'wait')
        return 'Wait'
      return t
    })
    const isTerminalWait = (): boolean => tool() === 'wait' && status() !== CODEX_STATUS.IN_PROGRESS && status() !== ''
    const isWaitInProgress = (): boolean => tool() === 'wait' && !isTerminalWait()
    const isSpawnAgent = (): boolean => tool() === 'spawnAgent'
    const hasPrompt = (): boolean => prompt().trim() !== ''
    const hasCollapsiblePrompt = (): boolean => (isWaitInProgress() || isSpawnAgent()) && hasPrompt()
    const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.CODEX_COLLAB_AGENT_TOOL_CALL)
    const modelLabel = (): string => {
      const m = model()
      return m ? (getCachedSettingsLabel('model', m) || m) : ''
    }
    const effortLabel = (): string => {
      const e = reasoningEffort()
      return e ? (getCachedSettingsLabel('effort', e) || e) : ''
    }
    const spawnAgentDetails = createMemo(() => joinMetaParts([
      modelLabel() && `model: ${modelLabel()}`,
      effortLabel() && `reasoning effort: ${effortLabel()}`,
    ]))
    const titleEl = createMemo(() => {
      if (isTerminalWait())
        return `Wait ${status()}`
      if (isWaitInProgress())
        return 'Waiting for subagent'
      if (isSpawnAgent())
        return spawnAgentDetails() ? `Subagent (${spawnAgentDetails()})` : 'Subagent'
      return renderAgentTitle(displayName()) || codexStatusTitle(displayName(), status())
    })
    const promptHtml = createMemo(() => hasCollapsiblePrompt() ? renderMarkdown(prompt()) : '')
    const summary = (): JSX.Element | undefined => {
      if (expanded())
        return undefined
      if (hasCollapsiblePrompt())
        return <div class={`${toolResultContent} ${toolResultCollapsed} ${markdownContent}`} innerHTML={promptHtml()} />
      if (isWaitInProgress() || isTerminalWait() || isSpawnAgent() || !status())
        return undefined
      return <div class={toolInputSummary}>{status()}</div>
    }

    return (
      <ToolUseLayout
        icon={Bot}
        toolName={displayName()}
        title={titleEl()}
        summary={summary()}
        context={props.context}
        expanded={expanded()}
        onToggleExpand={hasCollapsiblePrompt() ? () => setExpanded(v => !v) : undefined}
      >
        <Show when={hasCollapsiblePrompt()}>
          <div class={`${toolResultContent} ${markdownContent}`} innerHTML={promptHtml()} />
        </Show>
      </ToolUseLayout>
    )
  },
})
