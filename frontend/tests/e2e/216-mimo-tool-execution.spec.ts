import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, editToolCall, readToolCall } from './helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code tool execution', () => {
  mimoTest('a shell command renders as a tool card with its output', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    // The command text, the prompt and the reply state no `mimo-42`, so only the
    // command's own output can put it in a tool row.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'echo-call', 'echo "mimo-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // The output reaches the card from MiMo's own metadata, not from the model's
    // reply, which does not repeat it.
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'mimo-42' }).first()).toBeVisible()
    const railedRows = page.locator('[data-span-columns]:not([data-span-columns="0"]):visible')
    await expect(railedRows.first()).toBeVisible()
  })

  // MiMo refuses an edit of a file that the session has not read, so the script
  // creates the file, reads it and then edits it -- the order a real model takes.
  mimoTest('a read and an edit render the file body and the applied diff', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'seed-parity', 'printf "const parityBefore = 1\\n" > parity.ts')] },
      { toolCalls: [readToolCall(AgentProvider.MIMO_CODE, 'parity-read', 'parity.ts')] },
      { toolCalls: [editToolCall(AgentProvider.MIMO_CODE, 'parity-edit', { path: 'parity.ts', before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { text: 'I changed parity.ts.' },
    )
    await sendMessage(page, modelScript.prompt('Change parity.ts.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const diff = page.locator('[data-file-diff]:visible')
    await expect(diff.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
    await expect(diff.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
    // The read reached the model in MiMo's own numbered format, which only a read
    // that succeeded writes: the seed command states the line with no number. A
    // failed read would state MiMo's error instead, and the edit after it would
    // have failed too.
    const afterRead = (await modelScript.status()).requests.find(request => request.stepIndex === 2)
    expect(JSON.stringify(afterRead?.body)).toContain('1: const parityBefore = 1')
    await expect(messageContents(page).filter({ hasText: 'has not been read' })).toHaveCount(0)
  })
})
