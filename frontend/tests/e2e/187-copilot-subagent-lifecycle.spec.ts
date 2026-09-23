import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest.describe('copilot subagent lifecycle', () => {
  copilotTest('routes the prompt, response, and completion into the child tab', async ({
    authenticatedCopilotWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedCopilotWorkspace
    await expectNoRegistryRows(page)

    // The child's prompt carries the marker, so its own turn reaches this
    // script. The rule matches text the PARENT prompt does not carry, or the
    // parent's own turn would take this answer instead of the queued spawn.
    await modelScript.rule({
      name: 'the child answers with its exact word',
      // NOT anchored, unlike the other providers' child rules. Copilot prefixes
      // every user turn with a `<current_datetime>` block, so `^` never matches
      // and the CHILD's turn ate the parent's queued step instead. It needs no
      // anchor either: Copilot returns a tool result as a `tool` message, so a
      // parent turn's last USER text stays the original prompt and never
      // carries a copy of the child's.
      when: { user: 'Reply with exactly COPILOT_CHILD_PONG' },
      respond: { text: 'COPILOT_CHILD_PONG' },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.GITHUB_COPILOT, 'spawn-copilot', {
        description: 'Ask the subagent for one word',
        prompt: modelScript.prompt('Reply with exactly COPILOT_CHILD_PONG.'),
      })],
    })
    await modelScript.queue({ text: 'COPILOT_ROOT_DONE' })
    await sendMessage(page, modelScript.prompt('Spawn one subagent, wait for it, then report what it said.'))
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page, 180_000)

    // The spawn is scripted, so a missing row is a failure rather than the
    // model's discretion.
    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)

    await expect(userBubbles(page).filter({ hasText: 'COPILOT_CHILD_PONG' })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: /^COPILOT_CHILD_PONG$/ })).toBeVisible()
  })
})
