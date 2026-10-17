import { expect } from '@playwright/test'
import { CODEX_ITEM, CODEX_METHOD, CODEX_RAW_ITEM } from '../../../src/generated/contracts/codex-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { codexTest } from '../codex-fixtures'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { expectNoRegistryRows, requireRegistryRow } from '../helpers/subagentRegistry'
import { chooseSettingsOption, interruptButton, openWorkspace, sendMessage, toolCallRow, waitForSettingsIdle } from '../helpers/ui'

codexTest('resumes the input queue and reaches the same native session after interruption', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

codexTest('stops an actual native tool and reaches the same native session after interruption', async ({ native }, testInfo) => {
  // Codex emits an execution wrapper and its nested command.
  // The wrapper's native cancellation message remains visible with its own outcome.
  const readCalls = async () => {
    const agent = await currentNativeAgent(native)
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    const frames = snapshot.messages.map(nativeMessageBody)
    const rawItems = frames.flatMap((frame) => {
      if (!isObject(frame) || pickString(frame, 'method') !== CODEX_METHOD.RawResponseItemCompleted)
        return []
      const item = pickObject(pickObject(frame, 'params'), 'item')
      return item && pickString(item, 'call_id') === 'held-native-tool' ? [item] : []
    })
    const wrapper = rawItems.find(item => pickString(item, 'type') === CODEX_RAW_ITEM.CustomToolCall)
    const result = rawItems.find(item => pickString(item, 'type') === CODEX_RAW_ITEM.CustomToolCallOutput)
    const command = frames.find((frame) => {
      const item = pickObject(isObject(frame) ? frame : undefined, 'item')
      return pickString(item, 'type') === CODEX_ITEM.CommandExecution && pickString(item, 'command').includes('interrupt-started-')
    })
    return { wrapper, result, command }
  }
  await exerciseInterruptTurn(native, {
    kind: 'tool',
    expectedToolUses: 2,
    beforeInterrupt: async () => {
      await retryUntilPass(async () => {
        const { command } = await readCalls()
        expect(command, 'the Worker records the native command before interruption').toBeDefined()
      })
    },
  })
  for (const reload of [false, true]) {
    if (reload) {
      await native.page.reload()
      await openWorkspace(native.page, native.workspaceId)
    }
    const { wrapper, result, command } = await readCalls()
    expect(wrapper, 'the stored transcript retains the cancelled execution request').toBeDefined()
    expect(command, 'the wrapper starts a separate native shell call').toBeDefined()
    expect(pickString(result, 'output')).toMatch(/^aborted by user after \d+\.\ds$/)
    await expect(toolCallRow(native.page, 'held-native-tool')).toHaveAttribute('data-tool-status', 'cancelled')
    await expect(toolCallRow(native.page, 'held-native-tool')).toContainText('aborted by user after')
  }
  await testInfo.attach('native-interrupted-codex-calls', {
    body: JSON.stringify(await readCalls(), null, 2),
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
    await withCleanup(async () => {
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
        respond: { text: 'The report is ready.', gate: 'codex-interrupt-a-turn-hold-1' },
      })
      // The ROOT holds too: its turn has to be interruptible, and a root that
      // finished would clear the loading state this test asserts on.
      const spawn = await modelScript.queue({
        toolCalls: [spawnSubagentToolCall(AgentProvider.CODEX, 'spawn-interrupt-probe', {
          description: taskName.replaceAll('_', ' '),
          prompt: modelScript.prompt('Inspect the worker agent package and prepare a detailed report.'),
        })],
      })
      await modelScript.queue({ text: 'The child finished.', gate: 'codex-interrupt-a-turn-hold-2' })
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
    }, async () => {
      await finishCleanup([modelScript.releaseGateIfHeld('codex-interrupt-a-turn-hold-1'), modelScript.releaseGateIfHeld('codex-interrupt-a-turn-hold-2')])
    })
  })
})
