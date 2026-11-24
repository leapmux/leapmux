import { join } from 'node:path'
import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent, selectedAgentTabId } from '../helpers/nativeScenario'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { DROID_CHILD_SYSTEM } from './childIdentity'
import { exerciseNativeChildTranscript } from './childScenarios'

function childSendAndWait(childId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    childId,
    prompt,
    '--request-id',
    requestId,
    '--wait',
    '--all',
    '--timeout',
    '20s',
  ]
}

droidTest.describe('factory Droid subagents', () => {
  droidTest('opens a live child transcript and sends a follow-up from its tab', async ({ native }, testInfo) => {
    await exerciseNativeChildTranscript(native, testInfo, { followUp: true })
  })

  droidTest('returns exact replies for bound child sends and retries during later child work', async ({ native }, testInfo) => {
    const { page, modelScript, leapmuxServer } = native
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { workingDir } = await currentNativeAgent(native)
    await exerciseNativeChildTranscript(native, testInfo, { followUp: false })
    const childId = await selectedAgentTabId(page)

    const firstPrompt = 'DROID_CHILD_EXACT_FIRST confirm the exact child reply.'
    const firstReply = 'The first bound Droid child reply completed.'
    const secondPrompt = 'DROID_CHILD_EXACT_SECOND read the child note and wait.'
    const secondReply = 'The second bound Droid child reply completed.'
    const secondGate = 'droid-child-exact-later-gate'
    const note = join(workingDir, 'droid-child-note.txt')

    await modelScript.rule(
      {
        name: 'the bound Droid child answers its first keyed send',
        when: { system: DROID_CHILD_SYSTEM, body: firstPrompt },
        respond: { text: firstReply },
        once: true,
      },
      {
        name: 'the bound Droid child starts its later tool turn',
        when: { system: DROID_CHILD_SYSTEM, body: secondPrompt },
        respond: {
          toolCalls: [
            readToolCall(native.provider, 'droid-child-later-read', note),
          ],
        },
        once: true,
      },
      {
        name: 'the bound Droid child holds its later reply',
        when: {
          system: DROID_CHILD_SYSTEM,
          body: [secondPrompt, 'DROID_CHILD_READ_MARKER'],
        },
        respond: { text: secondReply, gate: secondGate },
        once: true,
      },
    )

    const first = childSendAndWait(childId, modelScript.prompt(firstPrompt), 'droid-child-idle-first')
    const firstResult = await runCLI(cli, first) as { input_id: string }
    expect(firstResult).toMatchObject({
      message: 'delivered',
      turn_outcome: 'completed',
      replies: [firstReply],
    })
    await expect(assistantBubbles(page).filter({ hasText: firstReply })).toHaveCount(1)

    const second = childSendAndWait(childId, modelScript.prompt(secondPrompt), 'droid-child-idle-second')
    const retryFirstWhileSecondRuns = async () => {
      try {
        await modelScript.waitForGate(secondGate)
        expect(await runCLI(cli, first)).toMatchObject({
          input_id: firstResult.input_id,
          turn_outcome: 'completed',
          replies: [firstReply],
        })
      }
      finally {
        await modelScript.releaseGateIfHeld(secondGate)
      }
    }
    const [secondResult] = await Promise.all([
      runCLI(cli, second),
      retryFirstWhileSecondRuns(),
    ])
    expect(secondResult).toMatchObject({
      message: 'delivered',
      turn_outcome: 'completed',
      replies: [secondReply],
    })
    await expect(assistantBubbles(page).filter({ hasText: secondReply })).toHaveCount(1)
  })

  droidTest('steers a running bound child and returns only the post-steer reply', async ({ native }, testInfo) => {
    const { page, modelScript, leapmuxServer } = native
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { workingDir } = await currentNativeAgent(native)
    await exerciseNativeChildTranscript(native, testInfo, { followUp: false })
    const childId = await selectedAgentTabId(page)

    const leadPrompt = 'DROID_CHILD_STEER_LEAD read the child note before steering.'
    const leadReply = 'The bound child started reading the note.'
    const steerPrompt = 'DROID_CHILD_STEER_FOLLOW include the steered summary.'
    const finalReply = 'The steered bound child completed with the summary.'
    const gate = 'droid-child-steer-gate'
    const note = join(workingDir, 'droid-child-note.txt')

    await modelScript.rule(
      {
        name: 'the bound Droid child starts a tool turn and holds for steering',
        when: { system: DROID_CHILD_SYSTEM, body: leadPrompt },
        respond: {
          gate,
          text: leadReply,
          toolCalls: [
            readToolCall(native.provider, 'droid-child-steer-read', note),
          ],
        },
        once: true,
      },
      {
        name: 'the bound Droid child answers after reading the steer',
        when: {
          system: DROID_CHILD_SYSTEM,
          body: [leadPrompt, steerPrompt],
        },
        respond: { text: finalReply },
        once: true,
      },
    )

    const lead = [
      'agent',
      'send',
      childId,
      modelScript.prompt(leadPrompt),
      '--request-id',
      'droid-child-steer-lead',
    ]
    const steer = [
      'agent',
      'send',
      childId,
      modelScript.prompt(steerPrompt),
      '--request-id',
      'droid-child-steer-follow',
    ]

    await withCleanup(async () => {
      await runCLI(cli, lead)
      await modelScript.waitForGate(gate)
      const queued = await runCLI(cli, steer) as { input_id: string }
      const row = queuedInputRow(page, steerPrompt)
      await steerButton(row).click()
      await expectSteerAccepted(page, row)
      await modelScript.releaseGateIfHeld(gate)
      await waitForAgentIdle(page)

      await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: finalReply })).toHaveCount(1)
      expect(await runCLI(cli, [
        ...steer,
        '--wait',
        '--all',
        '--timeout',
        '20s',
      ])).toMatchObject({
        input_id: queued.input_id,
        message: 'delivered',
        turn_outcome: 'completed',
        replies: [finalReply],
      })
      expect(await runCLI(cli, [
        ...lead,
        '--wait',
        '--all',
        '--timeout',
        '20s',
      ])).toMatchObject({
        turn_outcome: 'completed',
        replies: [leadReply, finalReply],
      })
    }, async () => {
      await modelScript.releaseGateIfHeld(gate)
    })
  })
})
