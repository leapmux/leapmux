import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { WorkerAgentInputService } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { exerciseSteerAfterTool, NATIVE_TOOL_STEERING_TEXT } from '../helpers/nativeToolSteering'
import { askUserQuestionToolCall, bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { applyPermissionPreset, assistantBubbles, messageContents, openWorkspace, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { junieTest } from '../junie-fixtures'
import { JUNIE_AGENT, nativeContext } from './scenarios'

const steerCases = [
  { name: 'returns the exact reply when a steer starts a native task', count: 1 },
  { name: 'returns the shared reply when two identical steers enter one task', count: 2 },
]

for (const { name, count } of steerCases) {
  junieTest(name, async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    const marker = uniqueMarker()
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const head = `ANSWERHEAD${marker}`
    const tail = `ANSWERTAIL${marker}`
    const reply = `STEEREDREPLY${marker}`
    const steering = `STEERINPUT${marker}`
    const gate = `junie-steer-${marker}`
    await withCleanup(async () => {
      const start = await modelScript.queue(
        {
          ...nativeTextStep(context, `${head} ${tail}`),
          text: `${head} ${tail}`,
          stream: {
            chunkChars: head.length,
            delayMs: 0,
            gates: [
              { afterChunk: 1, name: gate },
            ],
          },
        },
        nativeTextStep(context, reply),
      )
      const lead = [
        'agent',
        'send',
        agentId,
        modelScript.prompt('Stream the scripted answer.'),
        '--request-id',
        'junie-steer-lead',
      ]
      const { input_id: leadId } = await runCLI(cli, lead) as { input_id: string }
      await modelScript.waitForGate(gate)
      const prompt = modelScript.prompt(steering)
      const steers: { args: string[], inputId: string }[] = []
      for (let index = 0; index < count; index++) {
        const args = [
          'agent',
          'send',
          agentId,
          prompt,
          '--request-id',
          `junie-carry-steer-${index}`,
        ]
        const { input_id: inputId } = await runCLI(cli, args) as { input_id: string }
        const row = queuedInputRow(page, steering)
        await steerButton(row).click()
        await expectSteerAccepted(page, row)
        steers.push({ args, inputId })
      }
      expect(new Set(steers.map(steer => steer.inputId)).size).toBe(count)
      await modelScript.releaseGate(gate)
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      for (const steer of steers) {
        expect(await runCLI(cli, steer.args.concat('--wait', '--all', '--timeout', '20s'))).toMatchObject({
          id: agentId,
          input_id: steer.inputId,
          turn_outcome: 'completed',
          replies: [reply],
        })
      }
      expect(await runCLI(cli, lead.concat('--wait', '--all', '--timeout', '20s'))).toMatchObject({
        id: agentId,
        input_id: leadId,
        turn_outcome: 'completed',
        replies: [],
      })

      // Junie starts a native task inside the original ACP prompt.
      const next = await modelScript.requestAt(start + 1)
      expect(nativeScenarioModelContextText(context, next)).toContain(steering)
      for (const reload of [false, true]) {
        if (reload) {
          await page.reload()
          await waitForAgentIdle(page)
        }
        await expect(userBubbles(page).filter({ hasText: steering })).toHaveCount(count)
        await expect(assistantBubbles(page).filter({ hasText: reply })).toHaveCount(1)
        await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
        await expect(messageContents(page).filter({ hasText: 'Turn failed' })).toHaveCount(0)
      }
    }, async () => { await modelScript.releaseGateIfHeld(gate) })
  })
}

for (const control of ['permission', 'question'] as const) {
  junieTest(`reports a blocking ${control} to the steers whose native task requested it`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const workspaceId = authenticatedEmptyWorkspace.workspaceId
    const { agentId, workingDir } = await openProviderAgent(leapmuxServer, workspaceId, JUNIE_AGENT, {
      optionValues: { brave_mode: 'off' },
    })
    await openWorkspace(page, workspaceId)
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId,
    })
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const output = join(workingDir, 'junie-steer-approved.txt')
    const reply = 'The steered task completed.'
    const steerText = 'Act on the updated report.'
    const gate = 'hold-junie-lead-before-steer-permission'
    const replyGate = 'hold-junie-reply-after-control'
    const tool = control === 'permission'
      ? bashToolCall(context.provider, 'junie-steer-write', `printf 'approved' > ${quotePosixShellArgument(output)}`)
      : askUserQuestionToolCall(context.provider, 'junie-steer-question', [
          {
            question: 'Which one?',
            header: 'Pick',
            options: [
              { label: 'First', description: 'The first choice.' },
              { label: 'Second', description: 'The second choice.' },
            ],
          },
        ])
    const start = await modelScript.queue({
      ...nativeTextStep(context, 'The original plan is ready.'),
      text: 'The original plan is ready.',
      stream: {
        chunkChars: 12,
        delayMs: 0,
        gates: [
          { afterChunk: 1, name: gate },
        ],
      },
    }, {
      toolCalls: [tool],
    }, {
      ...nativeTextStep(context, reply),
      gate: replyGate,
    })
    const keyedSend = (text: string, key: string) => [
      'agent',
      'send',
      agentId,
      modelScript.prompt(text),
      '--request-id',
      key,
    ]
    const lead = keyedSend('Report the original plan.', 'junie-permission-lead')
    const steers = [
      keyedSend(steerText, 'junie-permission-first'),
      keyedSend(steerText, 'junie-permission-second'),
    ]
    const waitFlags = ['--wait', '--all', '--timeout', '15s']
    await withCleanup(async () => {
      const { input_id: leadId } = await runCLI(cli, lead) as { input_id: string }
      await modelScript.waitForGate(gate)
      const steerIds: string[] = []
      for (const args of steers) {
        const { input_id: inputId } = await runCLI(cli, args) as { input_id: string }
        const row = queuedInputRow(page, steerText)
        await steerButton(row).click()
        await expectSteerAccepted(page, row)
        steerIds.push(inputId)
      }
      expect(new Set(steerIds).size).toBe(2)
      await modelScript.releaseGate(gate)
      await waitForControlBanner(page)
      if (control === 'permission')
        expect(existsSync(output)).toBe(false)

      const waiting = async (replyInputId: string) => (await channel.callWorker(
        leapmuxServer.workerId,
        WorkerAgentInputService.method.listAgentInputQueue,
        { agentId, replyInputId },
      )).inputReply?.waitingForUser
      expect(await waiting(leadId)).toBe(false)
      for (const inputId of steerIds)
        expect(await waiting(inputId)).toBe(true)
      for (const [index, args] of steers.entries()) {
        expect(await runCLI(cli, args.concat(waitFlags))).toMatchObject({
          input_id: steerIds[index],
          state: 'waiting_for_user',
          replies: [],
        })
      }
      const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
      expect(requests).toHaveLength(1)
      if (control === 'permission') {
        await runCLI(cli, [
          'agent',
          'approve',
          agentId,
          requests[0]!.id,
        ])
      }
      else {
        await runCLI(cli, [
          'agent',
          'answer',
          agentId,
          requests[0]!.id,
          '--field',
          'question_0=Second',
        ])
      }
      await modelScript.waitForGate(replyGate)
      for (const inputId of steerIds)
        expect(await waiting(inputId)).toBe(false)
      await modelScript.releaseGate(replyGate)
      await modelScript.waitForSteps(start + 3)
      if (control === 'permission')
        expect(readFileSync(output, 'utf8')).toBe('approved')
      for (const [index, args] of steers.entries()) {
        expect(await runCLI(cli, args.concat(waitFlags))).toMatchObject({
          input_id: steerIds[index],
          turn_outcome: 'completed',
          replies: [reply],
        })
      }
      expect(await runCLI(cli, lead.concat(waitFlags))).toMatchObject({
        input_id: leadId,
        turn_outcome: 'completed',
        replies: [],
      })
    }, async () => {
      await modelScript.releaseGateIfHeld(gate)
      await modelScript.releaseGateIfHeld(replyGate)
    })
  })
}

junieTest('injects steering into the turn while its native command runs', async ({ native }) => {
  await exerciseSteerAfterTool(native, {
    prepare: () => applyPermissionPreset(native.page, 'bypass'),
  })
  await native.page.reload()
  await waitForAgentIdle(native.page)
  await expect(userBubbles(native.page).filter({ hasText: NATIVE_TOOL_STEERING_TEXT })).toHaveCount(1)
  await expect(assistantBubbles(native.page).filter({ hasText: 'finished steered' })).toHaveCount(1)
  await expect(native.page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
  await sendNativeAnswer(native, 'Reply once after the injected steering turn.', 'The next native turn completed.')
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/519.
