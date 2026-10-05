import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { junieAnswerToolCall, junieSubagentSubmitToolCall, readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, messageContents, openWorkspace, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest, openJunieAgent } from '../junie-fixtures'

junieTest.describe('Junie subagents and background tasks', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.JUNIE

  const CUSTOM_TASK = 'Read the marker file, then report its exact content.'

  const CUSTOM_READ_MARKER = 'JUNIE_CUSTOM_READ_MARKER'

  const CUSTOM_GATE = 'junie-custom-submit'

  function junieHousekeeping() {
    return [
      { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Subagent task' } },
      { name: 'junie-task-summary', when: { system: 'You are a task summarizer' }, respond: { text: '<summary>Junie stores sessions in Junie Home.</summary><title>Session history</title>' } },
    ]
  }

  junieTest('streams a custom child read result into its tab before the final answer', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId, workingDir } = await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const note = join(workingDir, 'junie-child-note.txt')
    writeFileSync(note, `${CUSTOM_READ_MARKER}\n`)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.rule(...junieHousekeeping())
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
    await modelScript.queue(
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
    await modelScript.waitForSteps(1)
    const row = await requireRegistryRow(page)
    await expect(row).toContainText('leapmux-e2e-child')
    await modelScript.waitForGate(CUSTOM_GATE)
    try {
      await expect(row).toHaveAttribute('data-status', 'running')
      const childTabID = await openChildTabFromRow(page, row)
      await expect(userBubbles(page).filter({ hasText: CUSTOM_TASK })).toHaveCount(1)
      await expect(messageContents(page).filter({ hasText: CUSTOM_READ_MARKER }).first()).toBeVisible()
      await tabById(page, agentId).click()
      await expect(messageContents(page).filter({ hasText: CUSTOM_READ_MARKER })).toHaveCount(0)
      await tabById(page, childTabID).click()
    }
    finally {
      await modelScript.releaseGate(CUSTOM_GATE)
    }

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_CUSTOM_CHILD_DONE' }).first()).toBeVisible()
    const rows = await messageContents(page).allTextContents()
    const promptIndex = rows.findIndex(text => text.includes(CUSTOM_TASK))
    const readIndex = rows.findIndex(text => text.includes(CUSTOM_READ_MARKER))
    const answerIndex = rows.findIndex(text => text.includes('JUNIE_CUSTOM_CHILD_DONE'))
    expect(promptIndex).toBeGreaterThanOrEqual(0)
    expect(readIndex).toBeGreaterThan(promptIndex)
    expect(answerIndex).toBeGreaterThan(readIndex)

    await tabById(page, agentId).click()
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_CUSTOM_ROOT_DONE' }).first()).toBeVisible()
    await expectRowBecomesFinal(page, await requireRegistryRow(page))
  })
})
