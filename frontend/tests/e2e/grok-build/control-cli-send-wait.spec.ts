import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { expectNativeImagePart } from '../helpers/attachmentModelProbe'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { ruleRequest } from '../helpers/mockModelScript'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent, nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { bashToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { writeToolImage } from '../helpers/toolImages'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { applyPermissionPreset, chooseSettingsOption, openWorkspace, tabById, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { GROK_AGENT, nativeContext } from './scenarios'

grokTest('returns the exact reply to an interjection in the agent-initiated parent turn', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const { id: agentId, workingDir } = await currentNativeAgent(native)
  await applyPermissionPreset(page, 'bypass')
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const childReport = 'The delegated count reached one hundred.'
  const initial = 'The parent received the delegated count.'
  const guidance = 'Add the count to the final report.'
  const answer = 'The final report records one hundred items.'
  const proof = 'The parent approval passed.'
  const output = join(workingDir, 'agent-initiated-parent-approval.txt')
  const quotedPath = quotePosixShellArgument(output)
  const gate = 'hold-grok-agent-initiated-parent'
  const wait = ['--wait', '--all', '--timeout', '20s']
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(guidance),
    '--request-id',
    'grok-native-parent-interjection',
  ]
  let releaseChild: (() => Promise<boolean>) | undefined
  await modelScript.rule({
    name: 'hold the parent after the child report',
    when: { user: childReport },
    respond: {
      text: initial,
      stream: {
        chunkChars: 12,
        delayMs: 0,
        gates: [
          { afterChunk: 1, name: gate },
        ],
      },
    },
    once: true,
  }, {
    name: 'request permission in the steered parent turn',
    when: { user: guidance },
    respond: {
      toolCalls: [
        bashToolCall(native.provider, 'grok-parent-steer-permission', `printf '%s' ${quotePosixShellArgument(proof)} > ${quotedPath} && cat ${quotedPath}`),
      ],
    },
    once: true,
  }, {
    name: 'return the steered parent reply',
    when: {
      lastMessage: { role: 'tool', text: proof },
    },
    respond: nativeTextStep(native, answer),
    once: true,
  })
  await modelScript.fallback({ text: 'The delegated report was recorded.' })
  await withCleanup(async () => {
    const child = await openHeldChildTab(native, {
      background: true,
      childTurn: { user: HELD_CHILD_TASK },
      heldAnswer: { text: childReport },
      rootTurnsAfterSpawn: [
        { text: 'The background count started.' },
      ],
    })
    releaseChild = child.release
    await tabById(page, agentId).click()
    await retryUntilPass(async () => {
      expect((await readNativeInputQueue(leapmuxServer, agentId)).activeTurn).toBe(false)
    })
    await chooseSettingsOption(page, 'approvalMode-ask')
    await waitForNativeOptionApplied(native, 'approvalMode', 'ask')
    await child.release()
    await modelScript.waitForGate(gate)
    await retryUntilPass(async () => {
      expect((await readNativeInputQueue(leapmuxServer, agentId)).activeTurn).toBe(true)
    })

    const { input_id: steerId } = await runCLI(cli, steer) as { input_id: string }
    const row = page.getByTestId(`queued-input-${steerId}`)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(gate)
    await waitForControlBanner(page)
    expect(existsSync(output)).toBe(false)
    const request = ruleRequest(await modelScript.status(), 'request permission in the steered parent turn')
    expect(nativeScenarioModelContextText(native, request)).toContain(initial)
    expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
      input_id: steerId,
      state: 'waiting_for_user',
      replies: [],
    })
    const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    await runCLI(cli, [
      'agent',
      'approve',
      agentId,
      requests[0]!.id,
    ])
    await waitForAgentIdle(page)
    expect(readFileSync(output, 'utf8')).toBe(proof)
    expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
      input_id: steerId,
      turn_outcome: 'completed',
      replies: [answer],
    })
  }, () => finishCleanup([
    modelScript.releaseGateIfHeld(gate),
    ...(releaseChild
      ? [
          releaseChild(),
        ]
      : []),
  ]))
})

for (const attachmentOnly of [false, true]) {
  const title = attachmentOnly
    ? 'returns the exact reply to an image-only interjection'
    : 'returns the exact reply to an interjection with text and an image'
  grokTest(title, async ({ native }) => {
    const { leapmuxServer, modelScript, page } = native
    const { id: agentId, workingDir } = await currentNativeAgent(native)
    const fileName = writeToolImage(workingDir, 'grok-interjection-boundary')
    const path = join(workingDir, fileName)
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const guidance = 'Include the label from this image.'
    const hold = 'hold-grok-lead-for-image'
    const reply = 'The attached image label is ready.'
    const steerText: string[] = []
    if (!attachmentOnly)
      steerText.push(modelScript.prompt(guidance))
    const steer = [
      'agent',
      'send',
      agentId,
      ...steerText,
      '--attach',
      path,
      '--request-id',
      'image-interjection',
    ]
    const plainSteer = [
      'agent',
      'send',
      agentId,
      ...steerText,
      '--request-id',
      'text-interjection-with-same-caption',
    ]
    const enqueue = async (args: string[]) => {
      const { input_id: inputId } = await runCLI(cli, args) as { input_id: string }
      const row = page.getByTestId(`queued-input-${inputId}`)
      await steerButton(row).click()
      await expectSteerAccepted(page, row)
      return inputId
    }
    const wait = ['--wait', '--all', '--timeout', '20s']

    await withCleanup(async () => {
      const start = await modelScript.queue({
        text: 'The original image report is pending.',
        stream: {
          chunkChars: 12,
          delayMs: 0,
          gates: [
            { afterChunk: 1, name: hold },
          ],
        },
      }, nativeTextStep(native, reply))
      await runCLI(cli, [
        'agent',
        'send',
        agentId,
        modelScript.prompt('Prepare an image report.'),
      ])
      await modelScript.waitForGate(hold)
      const steerId = await enqueue(steer)
      const plainId = attachmentOnly
        ? undefined
        : await enqueue(plainSteer)
      await modelScript.releaseGate(hold)
      await modelScript.waitForSteps(start + 2)
      const request = await modelScript.requestAt(start + 1)
      if (!attachmentOnly)
        expect(nativeScenarioModelContextText(native, request).split(guidance)).toHaveLength(3)
      await expectNativeImagePart(request, readFileSync(path), async () => {
        throw new Error('The Grok interjection must preserve the PNG bytes.')
      })
      await waitForAgentIdle(page)
      expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
        input_id: steerId,
        turn_outcome: 'completed',
        replies: [reply],
      })
      if (plainId !== undefined) {
        expect(plainId).not.toBe(steerId)
        expect(await runCLI(cli, plainSteer.concat(wait))).toMatchObject({
          input_id: plainId,
          turn_outcome: 'completed',
          replies: [reply],
        })
      }
    }, async () => { await modelScript.releaseGateIfHeld(hold) })
  })
}

grokTest('returns the exact reply to a native interjection', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const workspaceId = authenticatedEmptyWorkspace.workspaceId
  const { agentId } = await openProviderAgent(leapmuxServer, workspaceId, GROK_AGENT, {
    optionValues: { approvalMode: 'always-approve' },
  })
  await openWorkspace(page, workspaceId)
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId,
  })
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const before = 'The initial shipment report is ready.'
  const guidance = 'Also check the shipment checksum.'
  const reply = 'The shipment checksum is valid.'
  const hold = 'hold-the-grok-lead-for-interjection'
  const keyed = (text: string, key: string) => [
    'agent',
    'send',
    agentId,
    modelScript.prompt(text),
    '--request-id',
    key,
  ]
  const lead = keyed('Prepare the original shipment report.', 'grok-interjection-lead')
  const steer = keyed(guidance, 'grok-interjection-steer')
  const wait = ['--wait', '--all', '--timeout', '20s']

  await withCleanup(async () => {
    const start = await modelScript.queue({
      ...nativeTextStep(context, before),
      text: before,
      stream: {
        chunkChars: 12,
        delayMs: 0,
        gates: [
          { afterChunk: 1, name: hold },
        ],
      },
    }, nativeTextStep(context, reply))
    const { input_id: leadID } = await runCLI(cli, lead) as { input_id: string }
    await modelScript.waitForGate(hold)
    const { input_id: steerID } = await runCLI(cli, steer) as { input_id: string }
    expect(steerID).not.toBe(leadID)
    const row = queuedInputRow(page, guidance)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(hold)
    await modelScript.waitForSteps(start + 2)
    const receivingRequest = await modelScript.requestAt(start + 1)
    expect(nativeScenarioModelContextText(context, receivingRequest)).toContain(guidance)
    await waitForAgentIdle(page)

    const original = await runCLI(cli, lead.concat(wait)) as { replies: string[] }
    expect(original).toMatchObject({ input_id: leadID, turn_outcome: 'completed' })
    expect(original.replies.join(' ')).toContain(before)
    expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
      input_id: steerID,
      turn_outcome: 'completed',
      replies: [reply],
    })
  }, async () => { await modelScript.releaseGateIfHeld(hold) })
})

grokTest('keeps the replies to two identical interjections through a permission', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const workspaceId = authenticatedEmptyWorkspace.workspaceId
  const { agentId, workingDir } = await openProviderAgent(leapmuxServer, workspaceId, GROK_AGENT, {
    optionValues: { approvalMode: 'ask' },
  })
  await openWorkspace(page, workspaceId)
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId,
  })
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const guidance = 'Include the shipment manifest.'
  const hold = 'hold-grok-lead-for-identical-interjections'
  const output = join(workingDir, 'grok-steer-approval.txt')
  const command = `printf approved > ${quotePosixShellArgument(output)}`
  const reply = 'The shipment manifest was verified.'
  const wait = ['--wait', '--all', '--timeout', '20s']
  const steers = ['grok-identical-first', 'grok-identical-second'].map(key => [
    'agent',
    'send',
    agentId,
    modelScript.prompt(guidance),
    '--request-id',
    key,
  ])
  await withCleanup(async () => {
    const start = await modelScript.queue({
      text: 'This is the initial shipment report.',
      stream: {
        chunkChars: 12,
        delayMs: 0,
        gates: [
          { afterChunk: 1, name: hold },
        ],
      },
    }, {
      toolCalls: [
        bashToolCall(context.provider, 'grok-batch-approval', command),
      ],
    }, nativeTextStep(context, reply))
    await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Prepare the inventory report.'),
    ])
    await modelScript.waitForGate(hold)
    const ids: string[] = []
    for (const steer of steers) {
      const accepted = await runCLI(cli, steer) as { input_id: string }
      const row = queuedInputRow(page, guidance)
      await steerButton(row).click()
      await expectSteerAccepted(page, row)
      ids.push(accepted.input_id)
    }
    expect(new Set(ids).size).toBe(2)
    await modelScript.releaseGate(hold)
    await waitForControlBanner(page)
    const receiving = await modelScript.requestAt(start + 1)
    expect(nativeScenarioModelContextText(context, receiving).split(guidance)).toHaveLength(3)
    expect(existsSync(output)).toBe(false)
    for (const [index, steer] of steers.entries()) {
      expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
        input_id: ids[index],
        state: 'waiting_for_user',
        replies: [],
      })
    }
    const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    await runCLI(cli, [
      'agent',
      'approve',
      agentId,
      requests[0]!.id,
    ])
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)
    expect(readFileSync(output, 'utf8')).toBe('approved')
    for (const [index, steer] of steers.entries()) {
      expect(await runCLI(cli, steer.concat(wait))).toMatchObject({
        input_id: ids[index],
        turn_outcome: 'completed',
        replies: [reply],
      })
    }
  }, async () => { await modelScript.releaseGateIfHeld(hold) })
})

grokTest('returns the reply to an interjection that Grok reads after a native tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const workspaceId = authenticatedEmptyWorkspace.workspaceId
  const { agentId, workingDir } = await openProviderAgent(leapmuxServer, workspaceId, GROK_AGENT, {
    optionValues: { approvalMode: 'always-approve' },
  })
  await openWorkspace(page, workspaceId)
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId,
  })
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const tool = createToolOutputControl(workingDir)
  const guidance = 'Include the checksum in the inventory.'
  const reply = 'The inventory checksum is valid.'
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(guidance),
    '--request-id',
    'grok-tool-steer',
  ]
  await withCleanup(async () => {
    const start = await modelScript.queue({
      toolCalls: [
        bashToolCall(context.provider, 'grok-interjection-tool', tool.command),
      ],
    }, nativeTextStep(context, reply))
    await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Run the inventory checkpoint command.'),
    ])
    await modelScript.waitForSteps(start + 1)
    await tool.waitForFirstOutput()
    const { input_id: steerId } = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, guidance)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await tool.releaseFirstOutput()
    await tool.waitForSecondOutput()
    await tool.releaseFinalOutput()
    await modelScript.waitForSteps(start + 2)
    expect(nativeScenarioModelContextText(context, await modelScript.requestAt(start + 1))).toContain(guidance)
    await waitForAgentIdle(page)
    expect(await runCLI(cli, steer.concat('--wait', '--all', '--timeout', '20s'))).toMatchObject({
      input_id: steerId,
      turn_outcome: 'completed',
      replies: [reply],
    })
  }, async () => {
    await tool.releaseFirstOutput()
    await tool.releaseFinalOutput()
  })
})
