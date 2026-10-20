import { expect } from '@playwright/test'
import { nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { exerciseSteerAfterTool, exerciseSteerDuringText } from '../helpers/nativeToolSteering'
import { uniqueMarker } from '../helpers/shellArguments'
import { steerQueuedInput } from '../helpers/steer'
import { assistantBubbles, interruptButton, openWorkspace, resumePausedQueue, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { KIRO_AGENT, nativeContext } from './scenarios'

kiroTest.describe('Kiro interrupt, steering and process lifetime', () => {
  // The allow-all preset runs the command with no permission banner. Kiro counts its live progress in tokens
  // (`kiro/generation-progress.spec.ts`), and no spec proves that it draws a running command's output, so the
  // proof reads the output from the model request alone.
  kiroTest('steers a running turn with a queued message', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { policyPreset: 'allow-all' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseSteerAfterTool(context, { expectDisplayedOutput: false })
  })

  kiroTest('places a steer after the complete answer that precedes it', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { policyPreset: 'allow-all' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseSteerDuringText(context, { answer: 'continue', splitAnswer: true })
  })

  // A steer during a turn that then ends returns to the queue: Kiro clears
  // it without injecting it, and the next prompt reads it exactly once. The
  // turn ends by a stop, because Kiro injects a steer into the running turn
  // rather than preempting it, so the steer alone cannot end the turn.
  kiroTest('returns a steer to the queue after the turn ends', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { policyPreset: 'allow-all' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const marker = uniqueMarker()
    const steering = `STEERINPUT${marker}`
    const reply = `STEEREDREPLY${marker}`
    const gate = `kiro-queue-return-${marker}`
    const start = await modelScript.queue(
      { ...nativeTextStep(context, `ANSWERHEAD${marker}`), text: `ANSWERHEAD${marker}`, stream: { chunkChars: 10, delayMs: 0, gates: [{ afterChunk: 1, name: gate }] } },
      nativeTextStep(context, reply),
    )
    modelScript.allowUnconsumed('The stop ends the streamed answer after its first chunk.')
    await sendMessage(page, modelScript.prompt('Stream the scripted answer.'))
    await modelScript.waitForGate(gate)
    await steerQueuedInput(page, { message: modelScript.prompt(steering), match: steering })
    await interruptButton(page).click()
    // The stop pauses the queue; the cleared steer returns to it, and the
    // reader releases it for the next turn.
    await resumePausedQueue(page)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    const next = await modelScript.requestAt(start + 1)
    expect(nativeScenarioModelContextText(context, next), 'the next prompt reads the cleared steer exactly once').toContain(steering)
    for (const reload of [false, true]) {
      if (reload) {
        await page.reload()
        await waitForAgentIdle(page)
      }
      await expect(userBubbles(page).filter({ hasText: steering })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: reply })).toHaveCount(1)
    }
  })
})
