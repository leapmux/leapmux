import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { steerQueuedInput } from '../helpers/steer'
import { HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { applyPermissionPreset, assistantBubbles, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'

grokTest('delivers an exact message to the running native child and preserves its own transcript', async ({ native }, testInfo) => {
  const { page, modelScript } = native
  await applyPermissionPreset(page, 'bypass')
  const parent = await currentNativeAgent(native)
  const output = createToolOutputControl(parent.workingDir)
  const marker = uniqueMarker('GROKCHILDSTEER')
  const steering = `Also include ${marker} after the controlled command.`
  const answer = `The native child read ${marker}.`
  const rule = 'the native child reads its exact message'
  let releaseChild: (() => Promise<boolean>) | undefined
  await modelScript.rule({
    name: rule,
    when: { user: marker },
    respond: { text: answer },
    once: true,
  })
  await modelScript.fallback({ text: 'The native parent received its child report.' })
  await withCleanup(async () => {
    const child = await openHeldChildTab(native, {
      background: true,
      childTurn: { user: HELD_CHILD_TASK },
      heldAnswer: {
        toolCalls: [
          bashToolCall(native.provider, 'native-child-message-shell', output.command),
        ],
      },
      rootTurnsAfterSpawn: [
        { text: 'The native background child started.' },
      ],
    })
    releaseChild = child.release
    await child.release()
    await output.waitForFirstOutput()
    await steerQueuedInput(page, { message: steering, match: marker })
    await output.releaseFirstOutput()
    await output.waitForSecondOutput()
    await output.releaseFinalOutput()
    await expect.poll(async () => (await modelScript.status()).ruleMatches[rule] ?? 0).toBe(1)
    await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
    const status = await modelScript.status()
    const request = status.requests.find(request => request.rule === rule)
    if (!request)
      throw new Error('The native child made no request after its steered command.')
    const context = nativeModelContextText(request)
    expect(context).toContain(steering)
    expect(context).toContain(output.firstMarker)
    expect(context).toContain(output.secondMarker)
    expect(await child.heldTurns()).toBe(1)
    await child.finish()
    await expect(child.row).toHaveAttribute('data-status', 'succeeded')
    await testInfo.attach('grok-native-child-message', {
      body: JSON.stringify({ childId: child.childId, parentId: child.parentId, request }),
      contentType: 'application/json',
    })
    for (const reload of [false, true]) {
      if (reload)
        await page.reload()
      await tabById(page, child.childId).click()
      await waitForAgentIdle(page)
      await expect(userBubbles(page).filter({ hasText: steering })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
      await tabById(page, child.parentId).click()
      await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(0)
      await expect(userBubbles(page).filter({ hasText: steering })).toHaveCount(0)
    }
  }, () => finishCleanup([
    ...(releaseChild
      ? [
          releaseChild(),
        ]
      : []),
    output.releaseFirstOutput(),
    output.releaseFinalOutput(),
  ]))
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/517.
