import type { JSX } from 'solid-js'
import Bot from 'lucide-solid/icons/bot'
import { createMemo, Show } from 'solid-js'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { pickString } from '~/lib/jsonPick'
import { getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { markdownContent } from '../../../markdownEditor/markdownContent.css'
import { renderMarkdownForContext, useSharedExpandedState } from '../../../messageRenderers'
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
    // Default to the camelCase 'spawnAgent' the wire uses (tools are 'spawnAgent'/'wait'/
    // 'closeAgent'), so a payload that omits `tool` still matches the checks below
    // (isSpawnAgent, displayName) and renders the SpawnAgent layout rather than a bare title.
    const tool = (): string => (props.item.tool as string) || 'spawnAgent'
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
      return m ? (getCachedSettingsLabel(AgentProvider.CODEX, 'model', m) || m) : ''
    }
    const effortLabel = (): string => {
      const e = reasoningEffort()
      return e ? (getCachedSettingsLabel(AgentProvider.CODEX, 'effort', e) || e) : ''
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
    const promptHtml = createMemo(() => {
      if (!hasCollapsiblePrompt())
        return ''
      return renderMarkdownForContext(prompt(), props.context)
    })
    const summary = (): JSX.Element | undefined => {
      if (expanded())
        return undefined
      if (hasCollapsiblePrompt())
        return <div class={`${toolResultContent} ${toolResultCollapsed} ${markdownContent}`} ref={cachedInnerHtml(promptHtml)} />
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
          <div class={`${toolResultContent} ${markdownContent}`} ref={cachedInnerHtml(promptHtml)} />
        </Show>
      </ToolUseLayout>
    )
  },
})
