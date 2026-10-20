import { expect } from '@playwright/test'
import { nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { uniqueMarker } from '../helpers/shellArguments'
import { steerQueuedInput } from '../helpers/steer'
import { assistantBubbles, messageContents, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { junieTest } from '../junie-fixtures'
import { JUNIE_AGENT, nativeContext } from './scenarios'

junieTest('carries a steer into its own turn through _session/steering', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT)
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const marker = uniqueMarker()
  const head = `ANSWERHEAD${marker}`
  const tail = `ANSWERTAIL${marker}`
  const reply = `STEEREDREPLY${marker}`
  const steering = `STEERINPUT${marker}`
  const gate = `junie-steer-${marker}`
  const start = await modelScript.queue(
    { ...nativeTextStep(context, `${head} ${tail}`), text: `${head} ${tail}`, stream: { chunkChars: head.length, delayMs: 0, gates: [{ afterChunk: 1, name: gate }] } },
    nativeTextStep(context, reply),
  )
  await sendMessage(page, modelScript.prompt('Stream the scripted answer.'))
  await modelScript.waitForGate(gate)
  await steerQueuedInput(page, { message: modelScript.prompt(steering), match: steering })
  await modelScript.releaseGate(gate)
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)

  // Junie starts the steer as its own turn, so the steer rides the next
  // request and the second answer renders under a single divider.
  const next = await modelScript.requestAt(start + 1)
  expect(nativeScenarioModelContextText(context, next)).toContain(steering)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForAgentIdle(page)
    }
    await expect(userBubbles(page).filter({ hasText: steering })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: reply })).toHaveCount(1)
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: 'Turn failed' })).toHaveCount(0)
  }
})
