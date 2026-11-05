import { expect } from '@playwright/test'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { uniqueMarker } from '../helpers/shellArguments'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('preserves the complete streamed final answer after the turn ends and after reload', async ({ native }) => {
  await currentNativeAgent(native)
  const marker = uniqueMarker('FINALANSWER')
  const text = `${marker} begins the answer. ${'Preserve the final assistant message. '.repeat(10)}${marker} ends the answer.`
  const gate = `final-answer-${marker}`
  const step = await native.modelScript.queue({
    text,
    stream: {
      chunkChars: 48,
      delayMs: 0,
      gates: [
        { afterChunk: 1, name: gate },
      ],
    },
  })
  try {
    await sendMessage(native.page, native.modelScript.prompt('Stream the complete scripted answer.'))
    await native.modelScript.waitForGate(gate)
    await native.modelScript.releaseGate(gate)
    await native.modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(native.page)
    await expect(assistantBubbles(native.page).filter({ hasText: marker })).toHaveText([text])
    await native.page.reload()
    await expect(assistantBubbles(native.page).filter({ hasText: marker })).toHaveText([text])
  }
  finally {
    await native.modelScript.releaseGateIfHeld(gate)
  }
})
