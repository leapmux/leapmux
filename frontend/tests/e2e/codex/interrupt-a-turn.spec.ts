import { expect } from '@playwright/test'
import { CODEX_ITEM, CODEX_METHOD, CODEX_RAW_ITEM } from '../../../src/generated/contracts/codex-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { codexTest } from '../codex-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, requireRegistryRow } from '../helpers/subagentRegistry'
import { chooseSettingsOption, interruptButton, sendMessage, waitForSettingsIdle } from '../helpers/ui'

codexTest('resumes the input queue and reaches the same native session after interruption', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

codexTest('stops an actual native tool and reaches the same native session after interruption', async ({ native }, testInfo) => {
  // Codex emits a raw functions.exec call and its nested commandExecution call.
  // Both native calls count, even when the interrupt stops the command.
  await exerciseInterruptTurn(native, { kind: 'tool', expectedToolUses: 2 })
  const agent = await currentNativeAgent(native)
  const snapshot = await readNativeMessageSnapshot(native, agent.id)
  const frames = snapshot.messages.map(nativeMessageBody)
  const wrapper = frames.find((frame) => {
    if (!isObject(frame) || pickString(frame, 'method') !== CODEX_METHOD.RawResponseItemCompleted)
      return false
    const item = pickObject(pickObject(frame, 'params'), 'item')
    return pickString(item, 'type') === CODEX_RAW_ITEM.CustomToolCall && pickString(item, 'call_id') === 'held-native-tool'
  })
  const command = frames.find((frame) => {
    const item = pickObject(isObject(frame) ? frame : undefined, 'item')
    return pickString(item, 'type') === CODEX_ITEM.CommandExecution && pickString(item, 'command').includes('interrupt-started-')
  })
  expect(wrapper, 'the native execution wrapper reaches the Worker').toBeDefined()
  expect(command, 'the wrapper starts a separate native shell call').toBeDefined()
  await testInfo.attach('native-interrupted-codex-calls', {
    body: JSON.stringify({ wrapper, command }, null, 2),
    contentType: 'application/json',
  })
})

// Codex offers its question tool in the plan collaboration mode alone, as the agent-questions spec states.
codexTest('withdraws a waiting question and reaches the same native session after interruption', async ({ native }) => {
  await exerciseControlInterrupt(native, {
    control: 'question',
    prepare: async () => {
      await chooseSettingsOption(native.page, 'collaboration_mode-plan')
      await waitForSettingsIdle(native.page)
    },
  })
})

codexTest.describe('codex interrupted partial answer', () => {
  codexTest('keeps interrupted model text and its marker after reload', async ({ native }) => {
    await exerciseInterruptedPartialAnswer(native)
  })
})

codexTest.describe('codex interrupt', () => {
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
    const spawn = await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.CODEX, 'spawn-interrupt-probe', {
        description: taskName.replaceAll('_', ' '),
        prompt: modelScript.prompt('Inspect the worker agent package and prepare a detailed report.'),
      })],
    })
    await modelScript.queue({ text: 'The child finished.', delayMs: 120_000 })
    modelScript.allowUnconsumed('the interrupt ends the root turn while the child still runs')
    await sendMessage(page, modelScript.prompt('Spawn one child to inspect the package, then wait for it.'))
    await modelScript.waitForSteps(spawn + 1)

    const row = await requireRegistryRow(page)
    await expect(row).toHaveAttribute('data-status', 'running')

    const interruptBtn = interruptButton(page)
    await expect(interruptBtn).toBeVisible()
    await interruptBtn.click()
    await expect(interruptBtn).toHaveText('Interrupting...')

    await expect(page.locator('[data-testid="result-divider"]:visible').filter({ hasText: /^Turn interrupted$/ })).toBeVisible()
    await expect(interruptBtn).toBeEnabled()
    await expect(interruptBtn).toHaveText('Interrupt')
    await expect(row).toHaveAttribute('data-status', 'running')
  })
})
