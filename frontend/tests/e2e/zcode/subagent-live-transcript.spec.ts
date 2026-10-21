import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { editToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { messageContents, openWorkspace, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeChildToolTranscript } from './childScenario'
import { ZCODE_AGENT } from './scenarios'

zcodeTest('subagent-live-transcript: routes the prompt, tools, and final report into the child tab', async ({ native }) => {
  await exerciseZCodeChildToolTranscript(native)
})

zcodeTest('shows an automatic plan-mode denial in the child transcript only', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, ZCODE_AGENT, {
    directoryPrefix: 'zcode-child-denial-',
    optionValues: { [OPTION_ID_PERMISSION_MODE]: 'plan' },
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const agent = await currentNativeAgent({ page, leapmuxServer })
  const path = join(agent.workingDir, 'denial.txt')
  writeFileSync(path, 'before\n')
  const task = 'CHILD_DENIAL_TASK: attempt the scripted edit, then report the result.'
  const gate = 'zcode-child-denial-answer'
  // Plan reminders follow the child's task as separate user messages.
  // Match the actual task message, which the parent carries only inside tool arguments.
  const childWhen = { body: '"role":"user","content":"CHILD_DENIAL_TASK:' }
  await modelScript.rule(
    {
      name: 'the child attempts an edit',
      when: childWhen,
      respond: { toolCalls: [editToolCall(AgentProvider.ZCODE, 'denied-edit', { path, before: 'before', after: 'after' })] },
      once: true,
    },
    {
      name: 'the child reports its denial',
      when: childWhen,
      respond: { text: 'The edit was denied.', gate },
    },
  )
  await modelScript.queue(
    { toolCalls: [spawnSubagentToolCall(AgentProvider.ZCODE, 'denial-child', { description: 'Check the plan restriction', prompt: modelScript.prompt(task) })] },
    { text: 'The child restriction check ended.' },
  )
  await withCleanup(async () => {
    await sendMessage(page, modelScript.prompt('Run one child to check the plan restriction.'))
    await modelScript.waitForGate(gate)
    await expect(messageContents(page).filter({ hasText: 'Denied Edit' })).toHaveCount(0)
    const row = await requireRegistryRow(page)
    await openChildTabFromRow(page, row)
    await expect(messageContents(page).filter({ hasText: 'Denied Edit' })).toHaveCount(1)
    await modelScript.releaseGate(gate)
    await tabById(page, agentId).click()
    await waitForAgentIdle(page)
    await expect(messageContents(page).filter({ hasText: 'Denied Edit' })).toHaveCount(0)
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})
