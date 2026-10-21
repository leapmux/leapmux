import { expect } from '@playwright/test'
import { nativeTextStep } from '../helpers/nativeScenario'
import { exerciseSteerBeforeTool, exerciseSteerDuringText } from '../helpers/nativeToolSteering'
import { uniqueMarker } from '../helpers/shellArguments'
import { steerQueuedInput } from '../helpers/steer'
import { assistantBubbles, interruptButton, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native)
})

reasonixTest('places a steer after the complete answer that precedes it', async ({ native }) => {
  await exerciseSteerDuringText(native, { answer: 'continue', splitAnswer: true })
})

// A stop with a steer still pending makes Reasonix warn, as reply text, that
// the guidance did not apply: Reasonix consumes the steer and does not requeue
// it, so the worker's queue keeps nothing back. The warning is its own status
// row, and no answer bubble carries the "[warning]" prefix.
reasonixTest('states the warning of a stopped steer as a status row, not reply text', async ({ native }) => {
  const { page, modelScript } = native
  const marker = uniqueMarker()
  const steering = `PENDINGSTEER${marker}`
  const gate = `reasonix-stop-steer-${marker}`
  await modelScript.queue({ ...nativeTextStep(native, `HELDANSWER${marker}`), gate })
  await sendMessage(page, modelScript.prompt('Hold the scripted answer for a stop.'))
  await modelScript.waitForGate(gate)
  await steerQueuedInput(page, { message: modelScript.prompt(steering), match: steering })
  await interruptButton(page).click()
  await waitForAgentIdle(page)
  modelScript.allowUnconsumed('the stop ended the turn before the model read the held answer or the steer')
  await expect(assistantBubbles(page).filter({ hasText: '[warning]' })).toHaveCount(0)
  await expect(messageContents(page).filter({ hasText: 'Guidance was not applied' })).toHaveCount(1)
})
