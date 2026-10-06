import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { junieAnswerToolCall, junieSubagentSubmitToolCall, readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, expectRowsInOrder, messageContents, openWorkspace, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect, junieTest } from '../junie-fixtures'
import { JUNIE_AGENT } from './scenarios'

junieTest.describe('Junie subagents and background tasks', () => {
  const PROVIDER = AgentProvider.JUNIE

  const CUSTOM_TASK = 'Read the marker file, then report its exact content.'

  const CUSTOM_READ_MARKER = 'JUNIE_CUSTOM_READ_MARKER'

  const CUSTOM_GATE = 'junie-custom-submit'

  junieTest('streams a custom child read result into its tab before the final answer', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId, workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT)
    const note = join(workingDir, 'junie-child-note.txt')
    writeFileSync(note, `${CUSTOM_READ_MARKER}\n`)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.rule(
      {
        name: 'the custom Junie child reads the marker file',
        when: { system: 'You are the LeapMux test subagent', body: CUSTOM_TASK },
        respond: { toolCalls: [readToolCall(PROVIDER, 'junie-custom-read', note)] },
        once: true,
      },
      {
        name: 'the custom Junie child submits the marker',
        when: { system: 'You are the LeapMux test subagent', body: CUSTOM_READ_MARKER },
        respond: {
          gate: CUSTOM_GATE,
          toolCalls: [junieSubagentSubmitToolCall('junie-custom-submit', '### Summary\n- JUNIE_CUSTOM_CHILD_DONE: I read the marker file.\n### Changes\n- No files changed.\n### Verification\n- Read the marker file.')],
        },
        once: true,
      },
    )
    const start = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'junie-custom-spawn', {
          description: 'Read the marker file',
          prompt: modelScript.prompt(`${CUSTOM_TASK}\nPath: ${note}`),
          agentType: 'leapmux-e2e-child',
        })],
      },
      { toolCalls: [junieAnswerToolCall('junie-custom-root', 'JUNIE_CUSTOM_ROOT_DONE')] },
    )
    await sendMessage(page, modelScript.prompt('Delegate the marker file to the custom child, then report.'))
    await modelScript.waitForSteps(start + 1)
    const row = await requireRegistryRow(page)
    await expect(row).toContainText('leapmux-e2e-child')
    await modelScript.waitForGate(CUSTOM_GATE)
    await withCleanup(async () => {
      await expect(row).toHaveAttribute('data-status', 'running')
      const childTabID = await openChildTabFromRow(page, row)
      await expect(userBubbles(page).filter({ hasText: CUSTOM_TASK })).toHaveCount(1)
      await expect(messageContents(page).filter({ hasText: CUSTOM_READ_MARKER }).first()).toBeVisible()
      await tabById(page, agentId).click()
      await expect(messageContents(page).filter({ hasText: CUSTOM_READ_MARKER })).toHaveCount(0)
      await tabById(page, childTabID).click()
    }, () => modelScript.releaseGate(CUSTOM_GATE))

    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_CUSTOM_CHILD_DONE' }).first()).toBeVisible()
    await expectRowsInOrder(messageContents(page), [CUSTOM_TASK, CUSTOM_READ_MARKER, 'JUNIE_CUSTOM_CHILD_DONE'])

    await tabById(page, agentId).click()
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_CUSTOM_ROOT_DONE' }).first()).toBeVisible()
    await expectRowBecomesFinal(page, await requireRegistryRow(page))
  })
})
