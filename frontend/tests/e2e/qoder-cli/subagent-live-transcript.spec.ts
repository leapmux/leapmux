import type { Locator } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, visibleOnly } from '../helpers/ui'
import { expect, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI live child transcript', () => {
  const QODER = AgentProvider.QODER

  const CHILD_SYSTEM = 'You are an agent for Qoder'

  const CHILD_MARKER = 'QODER_CHILD_TOOL_MARKER'

  qoderTest('shows the child tool result before its final answer', async ({ authenticatedQoderWorkspace, page, modelScript }) => {
    const filename = 'qoder-child-note.txt'
    writeFileSync(join(authenticatedQoderWorkspace.workingDir, filename), CHILD_MARKER)
    const childTask = modelScript.prompt(`Read ${filename} and report its marker.`)
    const gate = 'qoder-child-final'

    await modelScript.rule(
      {
        name: 'the child reads its assigned file',
        when: { system: CHILD_SYSTEM, user: `Read ${filename}` },
        respond: { toolCalls: [readToolCall(QODER, 'child-read', filename)] },
        once: true,
      },
      {
        name: 'the child pauses before its final answer',
        when: { system: CHILD_SYSTEM, body: CHILD_MARKER },
        respond: { text: 'QODER_CHILD_FINAL', gate },
        once: true,
      },
    )
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(QODER, 'spawn-live-qoder', {
        description: 'Read the child note',
        prompt: childTask,
      })] },
      { text: 'The child finished its note.' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one subagent to read the child note.'))
    await modelScript.waitForGate(gate)

    let row: Locator | undefined
    try {
      const childRow = await requireRegistryRow(page)
      row = childRow
      await expect(childRow).toHaveAttribute('data-status', 'running')
      await expect.poll(async () => await childRow.getAttribute('data-child-agent-id') ?? '').not.toBe('')
      await openChildTabFromRow(page, childRow)
      await expect(userBubbles(page).filter({ hasText: `Read ${filename}` }).first()).toBeVisible()
      await expect(visibleOnly(page.getByText(CHILD_MARKER, { exact: false })).first()).toBeVisible()
      await expect(assistantBubbles(page).filter({ hasText: 'QODER_CHILD_FINAL' })).toHaveCount(0)
    }
    finally {
      await modelScript.releaseGate(gate)
    }

    await modelScript.waitForSteps()
    await expect(assistantBubbles(page).filter({ hasText: 'QODER_CHILD_FINAL' }).first()).toBeVisible()
    expect(row).toBeDefined()
    await expectRowBecomesFinal(page, row!)
  })
})
