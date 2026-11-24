import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { bashToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { expectSteerAccepted, steerButton } from '../helpers/steer'
import { HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { applyPermissionPreset, assistantBubbles, chooseSettingsOption, tabById, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

grokTest('delivers an exact message to the running native child and preserves its own transcript', async ({ native }, testInfo) => {
  const { page, modelScript, leapmuxServer } = native
  await applyPermissionPreset(page, 'bypass')
  const parent = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
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
    const sends = ['grok-native-child-steer-first', 'grok-native-child-steer-second'].map(key => [
      'agent',
      'send',
      child.childId,
      modelScript.prompt(steering),
      '--request-id',
      key,
    ])
    const inputIds: string[] = []
    for (const send of sends) {
      const { input_id: inputId } = await runCLI(cli, send) as { input_id: string }
      inputIds.push(inputId)
      const queued = page.getByTestId(`queued-input-${inputId}`)
      await steerButton(queued).click()
      await expectSteerAccepted(page, queued)
    }
    expect(new Set(inputIds).size).toBe(2)
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
    expect(context.split(steering)).toHaveLength(3)
    expect(context).toContain(output.firstMarker)
    expect(context).toContain(output.secondMarker)
    expect(await child.heldTurns()).toBe(1)
    await child.finish()
    await expect(child.row).toHaveAttribute('data-status', 'succeeded')
    for (const [index, send] of sends.entries()) {
      expect(await runCLI(cli, send.concat('--wait', '--all', '--timeout', '20s'))).toMatchObject({
        input_id: inputIds[index],
        turn_outcome: 'completed',
        replies: [answer],
      })
    }
    await testInfo.attach('grok-native-child-message', {
      body: JSON.stringify({ childId: child.childId, parentId: child.parentId, request }),
      contentType: 'application/json',
    })
    for (const reload of [false, true]) {
      if (reload)
        await page.reload()
      await tabById(page, child.childId).click()
      await waitForAgentIdle(page)
      await expect(userBubbles(page).filter({ hasText: steering })).toHaveCount(2)
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

grokTest('waits on the running child permission for an exact steer', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const parent = await currentNativeAgent(native)
  await applyPermissionPreset(page, 'bypass')
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const initial = createToolOutputControl(parent.workingDir)
  const file = join(parent.workingDir, 'grok-child-steer-approval.txt')
  const path = quotePosixShellArgument(file)
  const steering = 'Write the approved child record before reporting back.'
  const proof = 'The child command was approved.'
  const answer = 'The child record is ready.'
  const wait = ['--wait', '--all', '--timeout', '20s']
  let releaseChild: (() => Promise<boolean>) | undefined
  await modelScript.rule({
    name: 'request permission after reading the child steer',
    when: { user: steering },
    respond: {
      toolCalls: [
        bashToolCall(native.provider, 'native-child-steer-permission', `printf '%s' ${quotePosixShellArgument(proof)} > ${path} && cat ${path}`),
      ],
    },
    once: true,
  }, {
    name: 'return the approved child reply',
    when: {
      lastMessage: { role: 'tool', text: proof },
    },
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
          bashToolCall(native.provider, 'hold-native-child-before-permission', initial.command),
        ],
      },
      rootTurnsAfterSpawn: [
        { text: 'The background child started.' },
      ],
    })
    releaseChild = child.release
    await child.release()
    await initial.waitForFirstOutput()
    await tabById(page, child.parentId).click()
    await retryUntilPass(async () => {
      expect((await readNativeInputQueue(leapmuxServer, child.parentId)).activeTurn).toBe(false)
    })
    await chooseSettingsOption(page, 'approvalMode-ask')
    await waitForNativeOptionApplied(native, 'approvalMode', 'ask')
    await tabById(page, child.childId).click()
    const send = [
      'agent',
      'send',
      child.childId,
      modelScript.prompt(steering),
      '--request-id',
      'grok-child-steer-permission',
    ]
    const { input_id: inputId } = await runCLI(cli, send) as { input_id: string }
    const queued = page.getByTestId(`queued-input-${inputId}`)
    await steerButton(queued).click()
    await expectSteerAccepted(page, queued)
    await initial.releaseFirstOutput()
    await initial.waitForSecondOutput()
    await initial.releaseFinalOutput()

    await waitForControlBanner(page)
    expect(existsSync(file)).toBe(false)
    expect(await runCLI(cli, send.concat(wait))).toMatchObject({ input_id: inputId, state: 'waiting_for_user', replies: [] })
    const { requests } = await runCLI(cli, ['agent', 'requests', child.childId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    expect(await runCLI(cli, ['agent', 'requests', child.parentId])).toMatchObject({ requests: [] })
    await runCLI(cli, [
      'agent',
      'approve',
      child.childId,
      requests[0]!.id,
    ])
    await child.finish()
    expect(readFileSync(file, 'utf8')).toBe(proof)
    expect(await runCLI(cli, send.concat(wait))).toMatchObject({
      input_id: inputId,
      turn_outcome: 'completed',
      replies: [answer],
    })
  }, () => finishCleanup([
    ...(releaseChild
      ? [
          releaseChild(),
        ]
      : []),
    initial.releaseFirstOutput(),
    initial.releaseFinalOutput(),
  ]))
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/517.
