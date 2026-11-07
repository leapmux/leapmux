import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn, heldToolScript } from '../helpers/nativeLifecycle'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { isAlive } from '../helpers/processTree'
import { backgroundBashToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { backgroundTaskRows, expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { assistantBubbles, chooseSettingsOption, interruptButton, resumePausedQueue, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

for (const kind of ['model', 'tool'] as const) {
  antigravityTest(`interrupts a held native ${kind} turn and keeps the session usable`, async ({ native, modelScript }) => {
    const gate = `held-tool-continuation-${modelScript.id}`
    if (kind === 'tool') {
      // The shell can become asynchronous before the browser presses Interrupt.
      // Hold its native continuation so this case still interrupts the foreground.
      await modelScript.rule({
        name: 'hold the native continuation after the shell becomes a background task',
        when: {
          body: 'Tool is running as a background task with task id:',
        },
        respond: { text: 'The background shell remains held.', gate },
        once: true,
      })
    }
    await withCleanup(() => exerciseInterruptTurn(native, { kind }), async () => {
      await modelScript.releaseGateIfHeld(gate)
    })
  })
}

antigravityTest('stops the actual background shell after its foreground turn ends', async ({ native, page, modelScript }) => {
  await chooseSettingsOption(page, 'permissionMode-bypass')
  await waitForSettingsIdle(page)
  const marker = uniqueMarker()
  await sendNativeAnswer(native, `Keep BACKGROUNDCONTEXT${marker}.`, `BACKGROUNDANSWER${marker}`)
  const before = await currentNativeAgent(native)
  const startedFile = join(before.workingDir, `background-started-${marker}`)
  const releaseFile = join(before.workingDir, `background-release-${marker}`)
  const command = `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(heldToolScript({ startedFile, releaseFile }))}`
  let pid = 0
  // Native task completion can wake the model after cancellation, independently
  // of the explicit user turns below.
  await modelScript.fallback({ text: 'The native background task wake finished.' })
  await withCleanup(async () => {
    const start = await modelScript.queue(
      {
        toolCalls: [
          backgroundBashToolCall(native.provider, `background-shell-${marker}`, command),
        ],
      },
      { text: `FOREGROUNDENDED${marker}` },
    )
    await sendMessage(page, modelScript.prompt('Start the held background shell, then end the foreground turn.'))
    await modelScript.waitForSteps(start + 2)
    await expect(assistantBubbles(page).filter({ hasText: `FOREGROUNDENDED${marker}` })).toBeVisible()
    // Native background work can keep the cascade busy after the model's reply.
    // Its stored completed step proves that the foreground reply ended.
    await retryUntilPass(async () => {
      const snapshot = await readNativeMessageSnapshot(native, before.id)
      expect(snapshot.messages.some((message) => {
        const body = nativeMessageBody(message)
        if (!isObject(body) || body.type !== 'CORTEX_STEP_TYPE_PLANNER_RESPONSE' || body.status !== 'CORTEX_STEP_STATUS_DONE')
          return false
        const reply = pickObject(body, 'plannerResponse')
        return (pickString(reply, 'modifiedResponse') || pickString(reply, 'response')) === `FOREGROUNDENDED${marker}`
      })).toBe(true)
    })
    await expect.poll(() => existsSync(startedFile)).toBe(true)
    pid = Number(readFileSync(startedFile, 'utf8'))
    expect(Number.isSafeInteger(pid) && pid > 0).toBe(true)
    expect(isAlive(pid)).toBe(true)
    await expandBackgroundTasksSection(page)
    const row = backgroundTaskRows(page, { kind: 'shell' }).first()
    await expect(row).toHaveAttribute('data-status', 'running')
    await interruptButton(page).click()
    await expect.poll(() => isAlive(pid), { message: 'native cancellation stops the actual background process' }).toBe(false)
    await expect(row).toHaveAttribute('data-status', 'stopped')
    await waitForAgentIdle(page)
    await resumePausedQueue(page)
    const next = await sendNativeAnswer(native, 'Continue after stopping the background shell.', `BACKGROUNDCONTINUED${marker}`)
    expect(nativeModelContextText(next)).toContain(`BACKGROUNDANSWER${marker}`)
    expect((await currentNativeAgent(native)).agentSessionId).toBe(before.agentSessionId)
  }, async () => {
    writeFileSync(releaseFile, '')
    if (pid > 0)
      await expect.poll(() => isAlive(pid)).toBe(false)
  })
})

// A native permission request holds the turn until the reader answers or interrupts it.
antigravityTest('withdraws a waiting permission and keeps the session usable', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'permission' })
})

antigravityTest('keeps interrupted partial text and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})
