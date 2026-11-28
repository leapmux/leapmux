import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { ohMyPiAgentDirectory } from '../helpers/ohMyPiEnvironment'
import { createOutputGate } from '../helpers/outputGate'
import { backgroundBashToolCall, bashToolCall, ohMyPiYieldToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { getGlobalState } from '../helpers/server'
import { expectSteerAccepted, queuedInputRow, steerButton, steerQueuedInput } from '../helpers/steer'
import { openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset, assistantBubbles, messageContents, tabById, userBubbles } from '../helpers/ui'
import { OH_MY_PI_YOLO, ohMyPiTest } from '../ohmypi-fixtures'
import { ohMyPiChildTurn, ohMyPiHeldChild } from './childScenario'
import { nativeContext } from './scenarios'

ohMyPiTest('sends directly to a child waiting on a background job', async ({ page, modelScript, leapmuxServer, authenticatedEmptyWorkspace }) => {
  const { HOME: home, OMP_PROFILE: profile } = leapmuxServer.agentEnv
  if (!home || !profile)
    throw new Error('The idle-child scenario needs a private Oh My Pi profile.')
  const path = join(ohMyPiAgentDirectory(home, profile), 'config.yml')
  const config: unknown = JSON.parse(readFileSync(path, 'utf8'))
  if (!isObject(config))
    throw new Error('The idle-child scenario needs its native configuration.')
  await withNativeConfigurationFile({
    path,
    content: JSON.stringify({
      ...config,
      async: { enabled: true },
    }),
    runDir: getGlobalState().tmpDir,
  }, async () => {
    const native = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    const { workingDir } = await openNativeAgent(native, { overrides: OH_MY_PI_YOLO })
    const gate = createOutputGate(workingDir)
    const waiting = 'The child is waiting for its background job.'
    const prompt = 'Report the result of the idle check.'
    const reply = 'The idle check reached the child.'
    const jobOutput = 'OMP_IDLE_CHILD_JOB_OUTPUT'
    await modelScript.rule({
      name: 'the child waits for its open background job',
      when: {
        body: '"name":"yield"',
        lastMessage: { role: 'tool', text: 'Backgrounded as job' },
      },
      respond: { text: waiting },
      once: true,
    }, {
      name: 'the child answers its new direct input',
      when: ohMyPiChildTurn(prompt),
      respond: { text: reply },
      once: true,
    }, {
      name: 'the child finishes after its background job is released',
      when: {
        body: '"name":"yield"',
        lastMessage: { text: jobOutput },
      },
      respond: {
        toolCalls: [
          ohMyPiYieldToolCall('idle-child-yield', 'Background job completed'),
        ],
      },
      once: true,
    })
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const held = ohMyPiHeldChild()
    const child = await openHeldChildTab(native, {
      ...held,
      childTurn: {
        ...held.childTurn,
        lastMessage: { role: 'user' },
      },
      heldAnswer: {
        toolCalls: [
          backgroundBashToolCall(native.provider, 'idle-child-job', gate.hold(`printf %s ${jobOutput}`)),
        ],
      },
      rootTurnsAfterSpawn: [
        { text: 'The parent is waiting for the child.' },
        { text: 'The child completed the background job.' },
      ],
    })
    try {
      const expectChildTurn = (active: boolean) => retryUntilPass(async () => {
        expect((await readNativeInputQueue(leapmuxServer, child.childId)).activeTurn).toBe(active)
      })
      await expectChildTurn(true)
      await child.release()
      await expect(assistantBubbles(page).filter({ hasText: waiting })).toBeVisible()
      await expectChildTurn(false)
      const send = [
        'agent',
        'send',
        child.childId,
        modelScript.prompt(prompt),
        '--request-id',
        'omp-idle-child-direct',
        '--wait',
        '--all',
        '--timeout',
        '15s',
      ]
      expect(await runCLI(cli, send)).toMatchObject({
        id: child.childId,
        turn_outcome: 'completed',
        replies: [reply],
      })
      await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
    }
    finally {
      gate.release()
    }
    await child.finish()
  })
})

ohMyPiTest('returns the exact keyed reply after the steered child closes', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  await applyPermissionPreset(page, 'bypass')
  const prompt = 'Check whether the new shipment is ready.'
  const reply = 'The child confirmed the new shipment is ready.'
  await modelScript.rule({
    name: 'the child answers the exact steer',
    when: {
      body: ['"name":"yield"', prompt],
    },
    respond: {
      text: reply,
      toolCalls: [
        ohMyPiYieldToolCall('exact-child-yield', 'OMP_CHILD_EXACT'),
      ],
    },
    once: true,
  })
  const child = await openHeldChildTab(native, {
    ...ohMyPiHeldChild(),
    heldAnswer: {
      text: 'The child is still checking the original shipment.',
      toolCalls: [
        bashToolCall(native.provider, 'exact-child-ready', 'printf child-ready'),
      ],
    },
  })
  try {
    const send = [
      'agent',
      'send',
      child.childId,
      modelScript.prompt(prompt),
      '--request-id',
      'omp-child-exact-steer',
    ]
    const { input_id: inputId } = await runCLI(cli, send) as { input_id: string }
    const queued = queuedInputRow(page, prompt)
    await steerButton(queued).click()
    await expectSteerAccepted(page, queued)
    await child.finish()

    expect(await runCLI(cli, send.concat('--wait', '--all', '--timeout', '20s'))).toMatchObject({
      id: child.childId,
      input_id: inputId,
      turn_outcome: 'completed',
      replies: [reply],
    })
    await tabById(page, child.childId).click()
    await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: reply })).toHaveCount(1)
  }
  finally {
    await child.release()
  }
})

ohMyPiTest('delivers a steered message to the native child and keeps its parent usable', async ({ native }) => {
  const { page, modelScript } = native
  await applyPermissionPreset(page, 'bypass')
  const message = 'Also say OMP_CHILD_STEERED in your report.'
  await modelScript.rule({
    name: 'the child reads its steered message',
    when: {
      body: ['"name":"yield"', message],
    },
    respond: {
      toolCalls: [
        ohMyPiYieldToolCall('steered-child-yield', 'OMP_CHILD_STEERED'),
      ],
    },
    once: true,
  })
  const child = await openHeldChildTab(native, {
    ...ohMyPiHeldChild(),
    heldAnswer: {
      toolCalls: [
        bashToolCall(native.provider, 'child-ready', 'printf child-ready'),
      ],
    },
  })
  try {
    await steerQueuedInput(page, { message, match: 'Also say' })
    await child.finish()
    await expect.poll(async () => (await modelScript.status()).ruleMatches['the child reads its steered message'] ?? 0).toBe(1)
    await tabById(page, child.childId).click()
    await expect(userBubbles(page).filter({ hasText: message })).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'OMP_CHILD_STEERED' }).last()).toBeVisible()
    await tabById(page, child.parentId).click()
    await sendNativeAnswer(native, 'Confirm the parent still accepts input.', 'The parent still accepts input.')
  }
  finally {
    await child.release()
  }
})
