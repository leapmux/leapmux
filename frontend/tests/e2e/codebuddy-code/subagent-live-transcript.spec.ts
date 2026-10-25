import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { installGenerationObservation, parseGenerationCounters, waitForIncreasingCounter } from '../helpers/generationProgress'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { currentNativeAgent, expectSameNativeSession, nativeAgentById, selectedAgentTabId } from '../helpers/nativeScenario'
import { uniqueMarker } from '../helpers/shellArguments'
import { tabById } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code subagent registry', () => {
  codebuddyTest('shows the child prompt while the child still runs', async ({ native, authenticatedCodebuddyWorkspace }) => {
    const parent = await currentNativeAgent(native)
    const expectParentSession = async () => {
      const current = await nativeAgentById(native, parent.id)
      expect(current).toBeDefined()
      if (!current)
        throw new Error('The parent CodeBuddy agent disappeared during its child task.')
      expectSameNativeSession(parent, current, 'A native child task')
    }
    const suffix = uniqueMarker()
    const firstGate = `codebuddy-child-progress-first-${suffix}`
    const secondGate = `codebuddy-child-progress-second-${suffix}`
    await native.page.evaluate(installGenerationObservation)
    await withCleanup(async () => {
      await exerciseLiveChildTranscript(native, {
        childWhen: { user: 'Reply with CHILD_LIVE_DONE' },
        childTask: 'Reply with CHILD_LIVE_DONE.',
        parentTask: 'Delegate the live child task.',
        childResponse: {
          reasoning: 'The child reviews the native stream and reports each observed segment before its final answer.',
          text: 'CHILD_LIVE_DONE',
          stream: { chunkChars: 24, delayMs: 0, gates: [{ afterChunk: 1, name: firstGate }, { afterChunk: 2, name: secondGate }] },
        },
        toolProof: { read: { workingDir: authenticatedCodebuddyWorkspace.workingDir } },
        beforeRelease: expectParentSession,
        afterRelease: async () => {
          const childId = await selectedAgentTabId(native.page)
          await native.modelScript.waitForGate(firstGate)
          const first = await waitForIncreasingCounter(native, 'tokens')
          await tabById(native.page, parent.id).click()
          await expect.poll(async () => {
            const text = await native.page.locator('[data-testid="thinking-indicator"]:visible').textContent()
            return parseGenerationCounters(text ?? '').tokens ?? 0
          }, 'child model tokens stay out of the parent indicator').toBe(0)
          await tabById(native.page, childId).click()
          await native.page.evaluate(installGenerationObservation)
          await native.modelScript.releaseGate(firstGate)
          await native.modelScript.waitForGate(secondGate)
          await waitForIncreasingCounter(native, 'tokens', first)
          await native.modelScript.releaseGate(secondGate)
        },
        afterComplete: expectParentSession,
      })
    }, () => finishCleanup([
      native.modelScript.releaseGateIfHeld(firstGate),
      native.modelScript.releaseGateIfHeld(secondGate),
      native.page.evaluate(() => window.__nativeGenerationProbe?.stop()),
    ]))
  })
})
