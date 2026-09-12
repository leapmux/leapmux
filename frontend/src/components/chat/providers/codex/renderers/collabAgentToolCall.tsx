import Bot from 'lucide-solid/icons/bot'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import OctagonX from 'lucide-solid/icons/octagon-x'
import { createMemo, For, Show } from 'solid-js'
import { pickString } from '~/lib/jsonPick'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { AgentRequestMessage } from '../../../results/AgentRequestMessage'
import { AgentResultBody } from '../../../results/agentResult'
import { ToolHeaderRow } from '../../../results/ToolStatusHeader'
import { ToolMessageLayout } from '../../../widgets/ToolMessageLayout'
import { defineCodexRenderer } from '../defineRenderer'
import { codexAgentCounterpart, codexAgentRequest, codexAgentResults, resolveCodexAgentItem } from '../extractors/agent'

defineCodexRenderer({
  itemTypes: [CODEX_ITEM.COLLAB_AGENT_TOOL_CALL],
  render: (props) => {
    const isResult = () => props.context?.sources?.role() === 'result' || props.item.status !== CODEX_STATUS.IN_PROGRESS
    const request = () => codexAgentCounterpart(props.item, props.context?.sources?.request(), 'request')
    const result = () => codexAgentCounterpart(props.item, props.context?.sources?.result(), 'result')
    const resolved = createMemo(() => resolveCodexAgentItem(props.item, isResult() ? request() : result()))
    const source = createMemo(() => {
      const source = codexAgentRequest(resolved())
      const ids = resolved().receiverThreadIds
      if (resolved().tool === 'spawnAgent' && Array.isArray(ids) && ids.length === 1 && typeof ids[0] === 'string') {
        const title = props.context?.sources?.backgroundTask(ids[0])?.title
        if (title)
          return { ...source, description: title }
      }
      return source
    })
    const results = createMemo(() => codexAgentResults(resolved()))
    const failed = () => props.item.status === 'failed' || props.item.status === 'interrupted'
    return (
      <Show
        when={isResult()}
        fallback={(
          <AgentRequestMessage source={source()} hasResult={!!result()} context={props.context} />
        )}
      >
        <ToolMessageLayout role="result" hasRequest={!!request()} icon={Bot} toolName={source().toolName} title={source().description} context={props.context} alwaysVisible>
          <Show when={!props.context?.completionHeader && failed()}><ToolHeaderRow icon={props.item.status === 'interrupted' ? OctagonX : CircleAlert} title={props.item.status === 'interrupted' ? 'Interrupted' : 'Failed'} /></Show>
          <For each={results()}>{result => <AgentResultBody source={result} context={props.context} />}</For>
          <Show when={results().length === 0 && !failed()}><ToolHeaderRow icon={Check} title={pickString(props.item, 'status') || 'Result unavailable'} /></Show>
        </ToolMessageLayout>
      </Show>
    )
  },
})
