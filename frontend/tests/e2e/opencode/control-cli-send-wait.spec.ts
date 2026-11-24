import { existsSync } from 'node:fs'
import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent, nativeScenarioModelContextText } from '../helpers/nativeScenario'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset, assistantBubbles, sendMessage, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_CHILD, openCodeChildReportRule } from './childScenario'
import { outsideRemovalCommand, projectBesideProbe, withOutsideProjectAgent } from './outsideProject'

function keyedSend(agentId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    requestId,
  ]
}

function sendAndWait(agentId: string, prompt: string, requestId: string): string[] {
  return [
    ...keyedSend(agentId, prompt, requestId),
    '--wait',
    '--timeout',
    '20s',
  ]
}

opencodeTest('waits for the input that owns a permission request and replays its exact reply', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const { workingDir, file } = projectBesideProbe('opencode-cli-reply-', 'remove this test file')
  await withOutsideProjectAgent({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, workingDir, async (native) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { id: agentId } = await currentNativeAgent(native)
    const earlierAnswer = 'Two checks passed before approval.'
    const earlierStep = await modelScript.queue({ text: earlierAnswer })
    const earlier = sendAndWait(agentId, modelScript.prompt('Summarize the checks before approval.'), 'opencode-earlier-reply')
    const first = await runCLI(cli, earlier) as { input_id: string }
    expect(first).toMatchObject({
      turn_outcome: 'completed',
      replies: [earlierAnswer],
    })
    await modelScript.waitForSteps(earlierStep + 1)
    await waitForAgentIdle(page)

    const question = 'Run the scripted check outside the project directory.'
    const answer = 'The approved check completed.'
    const step = await modelScript.queue({
      toolCalls: [
        bashToolCall(native.provider, 'opencode-cli-permission', outsideRemovalCommand(file, 'OPENCODE_CLI_PERMISSION_')),
      ],
    }, { text: answer })
    const send = sendAndWait(agentId, modelScript.prompt(question), 'opencode-permission-reply')
    const blocked = await runCLI(cli, send) as { input_id: string }
    expect(blocked).toMatchObject({
      message: 'delivered',
      state: 'waiting_for_user',
    })
    await waitForControlBanner(page)
    expect(existsSync(file)).toBe(true)
    expect(await runCLI(cli, earlier)).toMatchObject({
      input_id: first.input_id,
      turn_outcome: 'completed',
      replies: [earlierAnswer],
    })

    const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    const [permission] = requests
    if (!permission)
      throw new Error('The Worker returned no OpenCode permission request.')
    await runCLI(cli, [
      'agent',
      'approve',
      agentId,
      permission.id,
    ])
    await modelScript.waitForSteps(step + 2)
    expect(await runCLI(cli, send)).toMatchObject({
      input_id: blocked.input_id,
      turn_outcome: 'completed',
      replies: [answer],
    })
    expect(existsSync(file)).toBe(false)
    await expect(userBubbles(page).filter({ hasText: question })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
  })
})

opencodeTest('returns exact reply ranges for a lead and steer in one execution', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const { workingDir, file } = projectBesideProbe('opencode-cli-steer-', 'remove only after approval')
  await withOutsideProjectAgent({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, workingDir, async (native) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { id: agentId } = await currentNativeAgent(native)
    const gate = 'opencode-exact-steer-lead'
    const leadReply = 'The original checklist passed.'
    const steerPrompt = 'Verify the additional checklist.'
    const steerReply = 'The additional checklist passed.'
    const lead = keyedSend(agentId, modelScript.prompt('Verify the original checklist.'), 'opencode-exact-steer-lead')
    const steer = keyedSend(agentId, modelScript.prompt(steerPrompt), 'opencode-exact-steer-input')
    const wait = ['--wait', '--all', '--timeout', '20s']
    const leadWait = [
      ...lead,
      ...wait,
    ]
    const steerWait = [
      ...steer,
      ...wait,
    ]
    const start = await modelScript.queue(
      { text: leadReply, gate },
      {
        toolCalls: [
          bashToolCall(native.provider, 'opencode-cli-steer-permission', outsideRemovalCommand(file, 'OPENCODE_STEER_PERMISSION_')),
        ],
      },
      { text: steerReply },
    )

    await withCleanup(async () => {
      await runCLI(cli, lead)
      await modelScript.waitForGate(gate)
      const queued = await runCLI(cli, steer) as { input_id: string }
      const row = queuedInputRow(page, steerPrompt)
      await steerButton(row).click()
      await expectSteerAccepted(page, row)
      await modelScript.releaseGate(gate)
      await modelScript.waitForSteps(start + 2)
      expect(nativeScenarioModelContextText(native, await modelScript.requestAt(start + 1))).toContain(steerPrompt)
      await waitForControlBanner(page)
      expect(existsSync(file)).toBe(true)
      expect(await runCLI(cli, steerWait)).toMatchObject({ input_id: queued.input_id, state: 'waiting_for_user' })
      expect(await runCLI(cli, leadWait)).toMatchObject({ state: 'waiting_for_user' })

      const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
      expect(requests).toHaveLength(1)
      await runCLI(cli, [
        'agent',
        'approve',
        agentId,
        requests[0]!.id,
      ])
      await modelScript.waitForSteps(start + 3)
      expect(await runCLI(cli, steerWait)).toMatchObject({
        input_id: queued.input_id,
        turn_outcome: 'completed',
        replies: [steerReply],
      })
      expect(await runCLI(cli, leadWait)).toMatchObject({
        turn_outcome: 'completed',
        replies: [leadReply, steerReply],
      })
      expect(existsSync(file)).toBe(false)
      await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(1)
    }, async () => { await modelScript.releaseGateIfHeld(gate) })
  })
})

opencodeTest('returns exact replies for a steer and an idle child input', async ({ native, leapmuxServer, page, modelScript }) => {
  await applyPermissionPreset(page, 'bypass')
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const gate = 'opencode-cli-child'
  const childTask = 'Run the marker before checking the child checklist.'
  const description = 'Check the child checklist'
  const steerPrompt = 'Verify the queued child checklist.'
  const steerReply = 'The queued child checklist passed.'
  const idlePrompt = 'Run the idle child checklist.'
  const idleReply = 'The idle child checklist passed.'
  await modelScript.rule({
    name: 'the child runs its marker tool',
    when: OPENCODE_CHILD.childTask(childTask),
    respond: {
      gate,
      toolCalls: [
        bashToolCall(native.provider, 'opencode-cli-child-tool', 'echo OPENCODE_CLI_CHILD_READY'),
      ],
    },
    once: true,
  }, {
    name: 'the running child reads its steer',
    when: { user: `^${steerPrompt}` },
    respond: { text: steerReply },
    once: true,
  }, {
    name: 'the idle child reads its new input',
    when: { user: `^${idlePrompt}` },
    respond: { text: idleReply },
    once: true,
  }, openCodeChildReportRule(description))

  await modelScript.queue({
    toolCalls: [
      spawnSubagentToolCall(native.provider, 'opencode-cli-child-spawn', {
        description,
        prompt: modelScript.prompt(childTask),
      }),
    ],
  }, { text: 'The parent received the child result.' })
  await withCleanup(async () => {
    await sendMessage(page, modelScript.prompt('Spawn one child to check the checklist.'))
    await modelScript.waitForGate(gate)
    const childRow = await requireRegistryRow(page)
    const childId = await openChildTabFromRow(page, childRow)
    const steer = keyedSend(childId, modelScript.prompt(steerPrompt), 'opencode-cli-child-steer')
    const queued = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, steerPrompt)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(gate)
    expect(await runCLI(cli, [
      ...steer,
      '--wait',
      '--timeout',
      '20s',
    ])).toMatchObject({
      id: childId,
      input_id: queued.input_id,
      turn_outcome: 'completed',
      replies: [steerReply],
    })
    const idle = sendAndWait(childId, modelScript.prompt(idlePrompt), 'opencode-cli-child-idle')
    expect(await runCLI(cli, idle)).toMatchObject({
      id: childId,
      turn_outcome: 'completed',
      replies: [idleReply],
    })
    await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: idleReply })).toHaveCount(1)
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})
