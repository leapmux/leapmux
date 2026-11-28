/**
 * End-to-end coverage for Codex-native `agent` CLI commands against a real
 * Codex process, Hub, Worker, and scripted mock model server:
 *
 *   agent retry-turn,
 *   agent approve-review,
 *   agent recoveries,
 *   agent recover.
 */

import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentInputQueuePauseReason } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { CODEX_AUTH_SAME_ACCOUNT, withCodexAuthEnvironment } from '../helpers/codexAuthEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { withNativeStartupWorker } from '../helpers/nativeStartupWorker'
import { codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, assistantBubbles, messageContents, openWorkspace, sendMessage, tabById, visibleOnly, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { CODEX_MANUAL_APPROVAL_PREFIX, codexAutoReviewRule, codexManualApproval } from './autoReview'
import { CODEX_AGENT, nativeContext, nativeLaunch } from './scenarios'

const FASTER_MODEL = 'gpt-5.6-sol'
const BUFFERED_GATE = 'cli-codex-buffered'
const BUFFERED_METADATA: Record<string, unknown>[] = [
  {
    type: 'safety_buffering',
    use_cases: ['cyber'],
    reasons: ['probes'],
    retry_model: FASTER_MODEL,
  },
]

codexTest.describe('control CLI Codex turn retry, review approval, goals, and recovery', () => {
  codexTest('retries a buffered Codex turn and exercises all five goal commands', async ({
    native,
    leapmuxServer,
  }) => {
    const agentId = (await currentNativeAgent(native)).id
    const { page, modelScript } = native
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    const start = await modelScript.queue(
      { responseMetadata: BUFFERED_METADATA, gate: BUFFERED_GATE, text: 'Slow buffered answer.' },
      nativeTextStep(native, 'Fast retried answer.'),
    )
    await withCleanup(async () => {
      await sendMessage(page, modelScript.prompt('Trigger safety buffering for CLI retry-turn.'))
      await modelScript.waitForGate(BUFFERED_GATE)
      await expect(visibleOnly(page.getByTestId('turn-retry-offer-banner'))).toHaveCount(1)

      const retried = await runCLI(cli, ['agent', 'retry-turn', agentId]) as {
        id: string
        turn_id: string
        model: string
      }
      expect(retried.id).toBe(agentId)
      expect(retried.turn_id).toBeTruthy()
      expect(retried.model).toBe(FASTER_MODEL)

      await modelScript.releaseGateIfHeld(BUFFERED_GATE)
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: 'Fast retried answer.' }).first()).toBeVisible()
    }, async () => {
      await modelScript.releaseGateIfHeld(BUFFERED_GATE)
    })

    // Exercise all 5 `agent goal` commands natively on Codex (get, set, pause, resume, clear).
    // Codex autonomously drives goal continuation turns while a goal is active, so install a fallback answer.
    await modelScript.fallback(nativeTextStep(native, 'Goal step noted.'))
    await runCLI(cli, [
      'agent',
      'goal',
      'set',
      agentId,
      modelScript.prompt('Keep Codex CLI goal E2E green.'),
    ])
    const goalActive = await runCLI(cli, ['agent', 'goal', 'get', agentId]) as {
      agent_id: string
      status?: string
    }
    expect(goalActive.agent_id).toBe(agentId)

    await runCLI(cli, ['agent', 'goal', 'pause', agentId])
    await runCLI(cli, ['agent', 'goal', 'resume', agentId])
    await runCLI(cli, ['agent', 'goal', 'clear', agentId])
    await waitForAgentIdle(page)
  })

  codexTest('records approval for a denied auto-review through agent approve-review', async ({
    native,
    leapmuxServer,
  }) => {
    const agentId = (await currentNativeAgent(native)).id
    const { page, modelScript } = native
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    const ruleName = 'cli-codex-auto-review-deny'
    await modelScript.rule(codexAutoReviewRule(ruleName, {
      outcome: 'deny',
      risk_level: 'high',
      user_authorization: 'low',
      rationale: 'CLI approve-review E2E denial.',
    }))
    await waitForSettingsHydrated(page, 'permissionMode')
    await applyPermissionPreset(page, 'smart')

    const file = join(createTestDirectory('cli-codex-review-'), 'denied.txt')
    const start = await modelScript.queue(
      {
        toolCalls: [
          codexEscalatedCommandToolCall('cli-review-call', `printf %s denied > ${quotePosixShellArgument(file)}`),
        ],
      },
      nativeTextStep(native, 'Auto-review denied the command.'),
    )
    await sendMessage(page, modelScript.prompt('Run escalated command for CLI approve-review.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect(existsSync(file)).toBe(false)

    // Discover the message_id and review_id from the live transcript via `agent messages --raw`.
    const msgs = await runCLI(cli, ['agent', 'messages', agentId, '--raw']) as {
      messages: Array<Record<string, unknown>>
    }
    let messageId = ''
    let reviewId = ''
    for (const msg of msgs.messages) {
      const text = JSON.stringify(msg)
      if (!text.includes(file))
        continue
      const rawMsg = (msg.raw ?? msg) as Record<string, unknown>
      const idMatch = /"id"\s*:\s*"([^"]+)"/.exec(text)
      const revMatch = /"reviewId"\s*:\s*"([^"]+)"|"review_id"\s*:\s*"([^"]+)"/.exec(text)
      if (revMatch) {
        messageId = String(rawMsg.id ?? msg.id ?? idMatch?.[1] ?? '')
        reviewId = revMatch[1] ?? revMatch[2] ?? ''
        break
      }
    }
    expect(messageId, `could not find denied review message id in ${JSON.stringify(msgs.messages)}`).toBeTruthy()
    expect(reviewId, `could not find denied review id in ${JSON.stringify(msgs.messages)}`).toBeTruthy()

    const approved = await runCLI(cli, [
      'agent',
      'approve-review',
      agentId,
      '--message-id',
      messageId,
      '--review-id',
      reviewId,
    ]) as { id: string, message_id: string, review_id: string }
    expect(approved).toEqual({ id: agentId, message_id: messageId, review_id: reviewId })

    const review = messageContents(page).getByTestId('auto-review').filter({ hasText: file })
    await expect(review.getByTestId('auto-review-approved')).toHaveText('Approved')

    const next = await modelScript.queue(nativeTextStep(native, 'Approval observed by model.'))
    await sendMessage(page, modelScript.prompt('Continue after CLI approve-review.'))
    await modelScript.waitForSteps(next + 1)
    await waitForAgentIdle(page)
    const approval = codexManualApproval(await modelScript.requestAt(next))
    expect(approval).toContain(CODEX_MANUAL_APPROVAL_PREFIX)
  })

  codexTest('lists and recovers a Codex agent paused for process recovery via agent recoveries and recover', async ({
    authenticatedEmptyWorkspace,
    page,
    modelScript,
    leapmuxServer,
  }) => {
    const initial = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    const passThrough = {
      ...nativeLaunch(initial),
      holdWhen: ['no-launch-is-held'],
      passThroughWhen: ['app-server'],
    }

    await withCodexAuthEnvironment({
      mockModelURL: leapmuxServer.mockModelUrl!,
      homeDir: createTestDirectory('cli-codex-recovery-auth-'),
    }, async (auth) => {
      await withNativeStartupWorker(initial, passThrough, {
        workerEnvironment: () => auth.workerEnvironment,
      }, async (workerId) => {
        const server = { ...leapmuxServer, workerId }
        const context = await nativeContext({
          page,
          modelScript,
          leapmuxServer: server,
          workspaceId: authenticatedEmptyWorkspace.workspaceId,
        })
        const cli = await mintCLITokenForAdmin(server)

        const opened = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, {
          directoryPrefix: 'cli-codex-recovery-',
        })
        await openWorkspace(page, context.workspaceId)
        await tabById(page, opened.agentId).click()

        // Establish a real native Codex session, then expire & re-login credentials on disk.
        await sendNativeAnswer(context, 'Initial turn before auth rotation.', 'Initial turn done.')
        const agent = await currentNativeAgent(context)

        const refusedBefore = auth.contentGenerations().length
        auth.expireAccessToken()
        await sendMessage(page, modelScript.prompt('Turn that triggers auth failure.'))
        await retryUntilPass(async () => {
          expect(auth.contentGenerations().slice(refusedBefore)).toEqual(['old-1', 'expired'])
        })
        await waitForAgentIdle(page)

        // Replace login on disk so the Worker enters PROCESS_RECOVERY when new input arrives.
        auth.login(CODEX_AUTH_SAME_ACCOUNT)
        await sendMessage(page, modelScript.prompt('Queued prompt waiting for CLI recovery.'))

        await retryUntilPass(async () => {
          const snapshot = await readNativeInputQueue(server, agent.id)
          expect(snapshot.pauseReason).toBe(AgentInputQueuePauseReason.PROCESS_RECOVERY)
        })

        // 1. `agent recoveries` lists the paused agent on this worker.
        const recoveries = await runCLI(cli, [
          'agent',
          'recoveries',
          '--provider',
          'codex',
          '--worker',
          workerId,
        ]) as { targets: Array<{ agent_id: string, provider: string }> }
        expect(recoveries.targets.some(t => t.agent_id === agent.id && t.provider === 'codex')).toBe(true)

        // 2. `agent recover` approves recovery and completes the queued prompt.
        const recoveredStep = await modelScript.queue(nativeTextStep(context, 'Recovered via CLI!'))
        await runCLI(cli, ['agent', 'recover', agent.id])
        await modelScript.waitForSteps(recoveredStep + 1)
        await waitForAgentIdle(page)
        await expect(assistantBubbles(page).filter({ hasText: 'Recovered via CLI!' }).first()).toBeVisible()
      })
    })
  })
})
