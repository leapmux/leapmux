import { join } from 'node:path'
import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, fileChangeRow, sendMessage, transcriptRows, waitForAgentIdle } from '../helpers/ui'
import { runWithoutApprovals } from './toolScenarios'

const CODEWHALE = AgentProvider.CODEWHALE

codewhaleTest.describe('Codewhale tool execution', () => {
  codewhaleTest('writes, edits and reads a file, and draws the edit as a diff', async ({ authenticatedCodewhaleWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedCodewhaleWorkspace
    const agent = await currentNativeAgent({ page, leapmuxServer })
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    const path = join(createNativeToolDirectory(agent.workingDir), 'notes.txt')
    await runWithoutApprovals(page)
    // The native calls use one literal private path and keep the original basename.
    await modelScript.queue(
      { toolCalls: [writeToolCall(CODEWHALE, 'write-call', { path, content: 'alpha\nold line\n' })] },
      { toolCalls: [editToolCall(CODEWHALE, 'edit-call', { path, before: 'old line', after: 'new line' })] },
      { toolCalls: [readToolCall(CODEWHALE, 'read-call', path)] },
      { text: 'notes.txt now holds the new line.' },
    )
    await sendMessage(page, modelScript.prompt('Create notes.txt, change its old line, and read it back.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The edit's header counts the runtime's own diff: one line out, one line in.
    const edit = fileChangeRow(page, 'notes.txt')
    await expect(edit).toBeVisible()
    await expect(edit.getByTestId('git-diff-stats')).toContainText('+1')
    await expect(edit.getByTestId('git-diff-stats')).toContainText('-1')
    // The diff itself is the one row that holds both sides of the change. The
    // prompt and the write hold the old line alone, and the read and the reply
    // hold the new line alone.
    await expect(transcriptRows(page).filter({ hasText: 'old line' }).filter({ hasText: 'new line' }).first()).toBeVisible()

    // The read draws the file as the edit left it.
    await expect(transcriptRows(page).filter({ hasText: 'alpha' }).filter({ hasText: 'new line' }).filter({ hasNotText: 'old line' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'notes.txt now holds the new line.' })).toBeVisible()
  })
})
