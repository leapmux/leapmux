import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { bashToolCall, editToolCall, readToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code tool execution', () => {
  // MiMo refuses an edit before the session reads the file.
  // The script creates the file. It reads the file before it edits the file.
  mimoTest('a read and an edit render the file body and the applied diff', async ({ authenticatedMiMoWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedMiMoWorkspace
    const agent = await currentNativeAgent({ page, leapmuxServer })
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    const path = join(createNativeToolDirectory(agent.workingDir), 'parity.ts')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'seed-parity', `printf "const parityBefore = 1\\n" > ${quotePosixShellArgument(path)}`)] },
      { toolCalls: [readToolCall(AgentProvider.MIMO_CODE, 'parity-read', path)] },
      { toolCalls: [editToolCall(AgentProvider.MIMO_CODE, 'parity-edit', { path, before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { text: 'I changed parity.ts.' },
    )
    await sendMessage(page, modelScript.prompt('Change parity.ts.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const diff = page.locator('[data-file-diff]:visible')
    await expect(diff.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
    await expect(diff.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
    // A successful native Read adds MiMo's numbered format to the model request. The seed command contains no line number.
    // If Read fails, Edit also fails because the session did not read the file.
    const afterRead = (await modelScript.status()).requests.find(request => request.stepIndex === 2)
    expect(JSON.stringify(afterRead?.body)).toContain('1: const parityBefore = 1')
    await expect(messageContents(page).filter({ hasText: 'has not been read' })).toHaveCount(0)
  })
})
