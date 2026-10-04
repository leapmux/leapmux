import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, waitForRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

const INTERRUPTION_MARKER = 'Text truncated by interruption.'

codexTest('resumes the input queue and reaches the same native session after interruption', async ({ authenticatedCodexWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseInterruptTurn({ page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId })
})

codexTest.describe('generation progress', () => {
  codexTest('keeps interrupted model text and its marker after reload', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    // STREAMED, not merely delayed. This test interrupts a turn mid-answer and
    // then asserts that the partial text survived, so the answer has to be
    // arriving while the interrupt lands: a step delivered in one piece leaves
    // nothing to truncate, and the token counter the assertion below reads never
    // moves. 240 pieces at 250 ms is a minute of answer, far longer than the
    // interrupt needs.
    const essay = 'Sorting algorithms compare, partition, and merge, and each choice costs time or space. '.repeat(30)
    await modelScript.queue({ text: essay, stream: { chunkChars: 10, delayMs: 250 } })
    modelScript.allowUnconsumed('the interrupt ends the turn before the streamed answer completes')
    await sendMessage(page, modelScript.prompt('Write a detailed explanation of sorting algorithms. Use no tools and start the answer immediately.'))

    const indicator = page.locator('[data-testid="thinking-indicator"]:visible')
    await expect(indicator).toContainText('tokens')
    await page.locator('[data-testid="interrupt-button"]:visible').click()
    await waitForAgentIdle(page, 120_000)

    await expect(assistantBubbles(page).filter({ hasText: INTERRUPTION_MARKER }).first()).toBeVisible()
    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: INTERRUPTION_MARKER }).first()).toBeVisible()
  })
})

codexTest.describe('codex interrupt', () => {
  codexTest('sends a prompt and interrupts mid-response', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // A held answer keeps the interrupt window open. The prompt no longer does:
    // the mock endpoint answers in milliseconds however long the request is, so
    // the window has to be stated rather than hoped for.
    await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
    modelScript.allowUnconsumed('the interrupt ends the turn before the held answer arrives')
    await sendMessage(page, modelScript.prompt('Write a very detailed essay about the history of computing.'))
    await modelScript.waitForSteps()

    // Click Interrupt. The button must appear within the test timeout.
    const interruptBtn = page.locator('[data-testid="interrupt-button"]')
    await expect(interruptBtn).toBeVisible()
    await interruptBtn.click()

    // After interrupt, the thinking indicator must clear, and at least
    // one user + partial-response bubble must be present.
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
    // And the button goes with it. The press publishes the stop, so neither
    // survives the provider's round trip, and there is nothing left to press.
    await expect(interruptBtn).not.toBeVisible()
    const bubbles = messageBubbles(page)
    expect(await bubbles.count()).toBeGreaterThan(1)
  })

  codexTest('stops loading after root interruption while a subagent remains active', async ({ authenticatedCodexWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedCodexWorkspace
    await expectNoRegistryRows(page, leapmuxServer)

    const taskName = 'interrupt_probe_child'
    // The child must still be RUNNING when the root is interrupted, which is the
    // whole subject: its answer is held far past the assertions below, so the
    // registry row cannot reach a final status while they run.
    //
    // Matched on the BODY, because `spawn_agent` forks the parent's conversation
    // and both agents therefore read the root's prompt as their last user turn.
    await modelScript.rule({
      name: 'the child works until the test ends',
      when: { body: ['NEW_TASK', taskName] },
      respond: { text: 'The report is ready.', delayMs: 120_000 },
    })
    // The ROOT holds too: its turn has to be interruptible, and a root that
    // finished would clear the loading state this test asserts on.
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.CODEX, 'spawn-interrupt-probe', {
        description: taskName.replaceAll('_', ' '),
        prompt: modelScript.prompt('Inspect the worker agent package and prepare a detailed report.'),
      })],
    })
    await modelScript.queue({ text: 'The child finished.', delayMs: 120_000 })
    modelScript.allowUnconsumed('the interrupt ends the root turn while the child still runs')
    await sendMessage(page, modelScript.prompt('Spawn one child to inspect the package, then wait for it.'))
    await modelScript.waitForSteps(1)

    const row = await waitForRegistryRow(page)
    await expect(row).toHaveAttribute('data-status', 'running')

    const interruptBtn = page.locator('[data-testid="interrupt-button"]:visible')
    await expect(interruptBtn).toBeVisible()
    await interruptBtn.click()
    await expect(interruptBtn).toHaveText('Interrupting...')

    await expect(page.locator('[data-testid="result-divider"]:visible').filter({ hasText: /^Turn interrupted$/ })).toBeVisible()
    await expect(interruptBtn).toBeEnabled()
    await expect(interruptBtn).toHaveText('Interrupt')
    await expect(row).toHaveAttribute('data-status', 'running')
  })
})
