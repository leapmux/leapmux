import type { Locator } from '@playwright/test'
import type { CodexAuthEnvironment } from '../helpers/codexAuthEnvironment'
import type { ManagedNativeScenarioContext, NativeContextFixtures, NativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeStartupLaunch, NativeStartupWrapper } from '../helpers/nativeStartupWrapper'
import type { NativeWorker } from '../helpers/nativeWorker'
import { expect } from '@playwright/test'
import { AgentInputQueuePauseReason, AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { CODEX_AUTH_OTHER_ACCOUNT, CODEX_AUTH_SAME_ACCOUNT, withCodexAuthEnvironment } from '../helpers/codexAuthEnvironment'
import { ruleRequest } from '../helpers/mockModelScript'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent, nativeModelConversationTurns, nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { withNativeStartupWorker } from '../helpers/nativeStartupWorker'
import { isAlive, listProcesses, withDescendants } from '../helpers/processTree'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { createTestDirectory } from '../helpers/runDirectory'
import { assistantBubbles, inputQueue, openWorkspace, queuePauseButton, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { CODEX_AGENT, nativeContext, nativeLaunch } from './scenarios'

/**
 * Authentication recovery with the real Codex CLI: an expired or replaced
 * ChatGPT login requires approval before replacing the affected process.
 * Recovery resumes the saved thread and delivers queued input once.
 * Native refreshes that succeed keep the process.
 *
 * Each scenario runs on its own private Worker whose Codex agents hold a
 * private CODEX_HOME with credentials the test controls; all content turns
 * stay scripted through the suite mock.
 */

/** The pass-through launch: hold nothing, run every Codex runtime directly. */
function passThroughLaunch(context: Parameters<typeof nativeLaunch>[0]): NativeStartupLaunch {
  const launch = nativeLaunch(context)
  return {
    ...launch,
    // The wrapper accepts one held runtime; recovery replaces processes at
    // any time, so every `app-server` launch runs for real. The hold word
    // matches nothing and must not overlap the pass-through word.
    holdWhen: ['no-launch-is-held'],
    passThroughWhen: ['app-server'],
  }
}

/**
 * The codex app-server processes that one private Worker owns, as pids. The
 * startup wrapper's own launcher also includes `app-server` in its command, so a
 * row under the wrapper directory is the wrapper, not a runtime.
 */
function codexProcessIds(workerDataDir: string, wrapperDirectory: string): number[] {
  const rows = listProcesses()
  const worker = rows.find(row => row.command.includes(workerDataDir))
  if (!worker)
    throw new Error(`The private Worker of ${workerDataDir} is absent from the process table.`)
  const owned = new Set(withDescendants(rows, [worker.pid]))
  return rows
    .filter(row => owned.has(row.pid) && row.command.includes('app-server') && !row.command.includes(wrapperDirectory))
    .map(row => row.pid)
    .sort((a, b) => a - b)
}

/** Require exactly one codex process and return its pid. */
function singleCodexPid(workerDataDir: string, wrapperDirectory: string): number {
  const pids = codexProcessIds(workerDataDir, wrapperDirectory)
  expect(pids, 'the tab owns exactly one codex process').toHaveLength(1)
  return pids[0]!
}

/** Send a prompt whose turn fails at the authentication layer, and wait for the turn to end. */
async function sendFailingTurn(
  context: NativeScenarioContext,
  auth: CodexAuthEnvironment,
  prompt: string,
  expectedGenerations: readonly string[] = ['old-1'],
): Promise<void> {
  const before = auth.contentGenerations().length
  await sendMessage(context.page, context.modelScript.prompt(prompt))
  await retryUntilPass(async () => expectContentGenerations(auth, before, expectedGenerations))
  await waitForAgentIdle(context.page)
}

/** Require the exact credential exchanges of one completed operation. */
function expectContentGenerations(auth: CodexAuthEnvironment, before: number, expected: readonly string[]): void {
  expect(auth.contentGenerations().slice(before), 'the operation made exactly the expected credential exchanges').toEqual(expected)
}

interface CodexAuthScenario {
  auth: CodexAuthEnvironment
  context: ManagedNativeScenarioContext
  server: ManagedNativeScenarioContext['leapmuxServer']
  wrapper: NativeStartupWrapper
  worker: NativeWorker<ManagedNativeScenarioContext['leapmuxServer']>
}

/** Own the private credentials and Worker for one browser scenario. */
async function withCodexAuthScenario(
  fixtures: NativeContextFixtures,
  directoryPrefix: string,
  use: (scenario: CodexAuthScenario) => Promise<void>,
): Promise<void> {
  const initial = await nativeContext(fixtures)
  await withCodexAuthEnvironment({
    mockModelURL: fixtures.leapmuxServer.mockModelUrl!,
    homeDir: createTestDirectory(directoryPrefix),
  }, async (auth) => {
    await withNativeStartupWorker(initial, passThroughLaunch(initial), {
      workerEnvironment: () => auth.workerEnvironment,
    }, async (workerId, wrapper, worker) => {
      const server = { ...fixtures.leapmuxServer, workerId }
      const context = await nativeContext({ ...fixtures, leapmuxServer: server })
      const workerPid = listProcesses().find(row => row.command.includes(worker.dataDir))?.pid
      expect(workerPid, 'the private Worker has a process identity').toBeDefined()
      await withCleanup(() => use({
        auth,
        context,
        server,
        wrapper,
        worker,
      }), async () => {
        for (const gate of (await context.modelScript.status()).pendingGates)
          await context.modelScript.releaseGateIfHeld(gate)
      })
      expect(listProcesses().find(row => row.command.includes(worker.dataDir))?.pid, 'the same private Worker still owns the scenario').toBe(workerPid)
    })
  })
}

/** Hold recovery at the model so stale-process cleanup precedes completion. */
async function sendRecoveredAnswer(
  context: ManagedNativeScenarioContext,
  auth: CodexAuthEnvironment,
  recovery: {
    stalePid: number
    generation: string
    prompt: string
    answer: string
    priorPrompt: string
    priorAnswer: string
    beforeApproval?: (dialog: Locator) => Promise<void>
  },
): Promise<void> {
  const { stalePid, generation, prompt, answer, priorPrompt, priorAnswer } = recovery
  const before = auth.contentGenerations().length
  const gate = `recovered-input-${before}`
  const stepIndex = await context.modelScript.queue({
    ...nativeTextStep(context, answer),
    gate,
  })
  await sendMessage(context.page, context.modelScript.prompt(prompt))
  await expectRecoveryInput(context)
  expect(isAlive(stalePid), 'the stale runtime remains alive before approval').toBe(true)
  let dialog = await openRecoveryDialog(context)
  if (recovery.beforeApproval) {
    await recovery.beforeApproval(dialog)
    expect(isAlive(stalePid), 'cancelling recovery leaves the stale runtime alive').toBe(true)
    await expectRecoveryInput(context)
    dialog = await openRecoveryDialog(context)
  }
  await approveRecovery(dialog)
  await context.modelScript.waitForGate(gate)
  expect(isAlive(stalePid), 'the stale runtime exited before the recovered answer completes').toBe(false)
  expectContentGenerations(auth, before, [generation])
  const request = await context.modelScript.requestAt(stepIndex)
  const text = nativeScenarioModelContextText(context, request)
  expect(text, 'the resumed request retains the prior prompt').toContain(priorPrompt)
  expect(text, 'the resumed request retains the prior answer').toContain(priorAnswer)
  const turns = nativeModelConversationTurns(request).filter(turn => turn.role === 'user')
  expect(turns.filter(turn => turn.text.includes(prompt)), 'the recovered input occurs in exactly one user turn').toHaveLength(1)
  expect(turns.at(-1)?.text).toContain(prompt)
  await context.modelScript.releaseGate(gate)
  await context.modelScript.waitForSteps(stepIndex + 1)
  await waitForAgentIdle(context.page)
  expectContentGenerations(auth, before, [generation])
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  await expect(queuePauseButton(context.page)).toHaveAttribute('aria-label', 'Pause Queue')
}

/** Require that unsubmitted input waits for approval of its current physical process. */
async function expectRecoveryInput(context: ManagedNativeScenarioContext) {
  const agent = await currentNativeAgent(context)
  const item = await retryUntilPass(async () => {
    const snapshot = await readNativeInputQueue(context.leapmuxServer, agent.id)
    expect(snapshot.pauseReason).toBe(AgentInputQueuePauseReason.PROCESS_RECOVERY)
    expect(snapshot.processRecovery?.outputGeneration).toBeGreaterThan(0n)
    expect(snapshot.processRecovery?.agentSessionId).toBe(agent.agentSessionId)
    expect(snapshot.processRecovery?.processInstanceId).toBeTruthy()
    const queued = snapshot.items.find(candidate => candidate.state === AgentInputState.QUEUED)
    expect(queued, 'the input waits without reaching the provider').toBeDefined()
    expect(queued?.error, 'the queue explains why approval is required').not.toBe('')
    return queued!
  })
  await expect(inputQueue(context.page).getByTestId(`queued-input-${item.id}`)).toBeVisible()
  return item
}

/** Open a confirmation without authorizing any process replacement. */
async function openRecoveryDialog(context: NativeScenarioContext): Promise<Locator> {
  await context.page.getByRole('button', { name: 'Recover login', exact: true }).filter({ visible: true }).first().click()
  const dialog = context.page.getByTestId('input-recovery-dialog')
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText('Any running work in the selected tabs and their subagents will be interrupted.')
  await expect(dialog).toContainText('Failed and uncertain inputs are not retried.')
  await expect(dialog.getByRole('checkbox', { name: 'Recover all Codex tabs with login issues' })).not.toBeChecked()
  await expect(dialog.getByTestId('input-recovery-summary')).toHaveText('1 Codex tab on 1 worker')
  await expect(dialog.getByRole('button', { name: 'Recover login', exact: true })).toBeEnabled()
  return dialog
}

/** Approve the captured processes and wait for their individual results. */
async function approveRecovery(dialog: Locator, recovered = 1, total = 1): Promise<void> {
  await dialog.getByRole('button', { name: 'Recover login', exact: true }).click()
  await expect(dialog.getByTestId('input-recovery-result-summary')).toHaveText(`Recovered ${recovered} of ${total} Codex ${total === 1
    ? 'tab'
    : 'tabs'}.`)
  await dialog.getByRole('button', { name: 'Done', exact: true }).click()
  await expect(dialog).not.toBeVisible()
}

/** Select one agent tab by its id: the tab bar's own tab, not the sidebar's leaf. */
async function selectAgent(context: NativeScenarioContext, agentId: string): Promise<void> {
  await tabById(context.page, agentId).click()
}

/** The saved native thread survives an operation. */
function expectSameThread(before: { agentSessionId: string }, after: { agentSessionId: string }): void {
  expect(before.agentSessionId, 'the original native thread has an identity').not.toBe('')
  expect(after.agentSessionId, 'the saved native thread survives').toBe(before.agentSessionId)
}

codexTest('keeps the process through a successful native token refresh', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withCodexAuthScenario({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, 'codex-auth-refresh-', async ({ auth, context, server, wrapper, worker }) => {
    const opened = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-refresh-' })
    await openWorkspace(page, context.workspaceId)
    await selectAgent(context, opened.agentId)

    // One successful turn establishes the process and the saved thread.
    await sendNativeAnswer(context, 'First turn before the access token expires.', 'The first turn completed before expiry.')
    const before = await currentNativeAgent(context)
    const pid = singleCodexPid(worker.dataDir, wrapper.directory)

    // The access token expires; the next turn refreshes it natively and
    // completes, and the turn after that pays no second refresh.
    auth.succeedNextRefresh()
    auth.expireAccessToken()
    const generationsBefore = auth.contentGenerations().length
    await sendNativeAnswer(context, 'Turn after the access token expired.', 'The native refresh completed the turn.')
    await sendNativeAnswer(context, 'Turn after the native refresh.', 'No second refresh was needed.')

    // The rejected request with the expired token, then two forwarded turns.
    expectContentGenerations(auth, generationsBefore, ['old-1', 'old-1', 'old-1'])
    expect(auth.contentGenerations()).toEqual(['old-1', 'old-1', 'old-1', 'old-1'])
    expect(auth.tokenEndpointCalls()).toBe(1)
    expect(singleCodexPid(worker.dataDir, wrapper.directory)).toBe(pid)
    expectSameThread(before, await currentNativeAgent(context))
  })
})

codexTest('recovers after approval when the refresh token expires and the same account logs in again', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withCodexAuthScenario({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, 'codex-auth-expired-', async ({ auth, context, server, wrapper, worker }) => {
    const opened = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-expired-' })
    await openWorkspace(page, context.workspaceId)
    await selectAgent(context, opened.agentId)
    await sendNativeAnswer(context, 'Turn before the login expired.', 'The healthy turn completed.')
    const before = await currentNativeAgent(context)
    const stalePid = singleCodexPid(worker.dataDir, wrapper.directory)

    // The access token expires and the refresh exchange is refused: the
    // turn fails inside the still-running process.
    const refusedBefore = auth.contentGenerations().length
    auth.expireAccessToken()
    // Native unauthorized recovery reloads the changed same-account token
    // and tries it before the refresh exchange reports permanent rejection.
    await sendFailingTurn(context, auth, 'Turn whose login cannot refresh.', ['old-1', 'expired'])
    expectContentGenerations(auth, refusedBefore, ['old-1', 'expired'])

    // A fresh login exists. Approval replaces the process and delivers the queued input once.
    auth.login(CODEX_AUTH_SAME_ACCOUNT)
    await sendRecoveredAnswer(context, auth, {
      stalePid,
      generation: 'old-2',
      prompt: 'Turn after signing in again.',
      answer: 'The replaced process resumed the saved thread.',
      priorPrompt: 'Turn before the login expired.',
      priorAnswer: 'The healthy turn completed.',
      beforeApproval: async (dialog) => {
        await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(dialog).not.toBeVisible()
      },
    })
    expectSameThread(before, await currentNativeAgent(context))
    expect(singleCodexPid(worker.dataDir, wrapper.directory)).not.toBe(stalePid)
    expect(auth.contentGenerations()).toEqual(['old-1', 'old-1', 'expired', 'old-2'])
  })
})

codexTest('recovers after approval following logout and login with a different account', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withCodexAuthScenario({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, 'codex-auth-switch-', async ({ auth, context, server, wrapper, worker }) => {
    const opened = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-switch-' })
    await openWorkspace(page, context.workspaceId)
    await selectAgent(context, opened.agentId)
    await sendNativeAnswer(context, 'Turn under the first account.', 'The first account answered.')
    const before = await currentNativeAgent(context)
    const stalePid = singleCodexPid(worker.dataDir, wrapper.directory)

    // A logout followed by a login as another account: the running process
    // keeps the old login, and its next turn fails.
    const refusedBefore = auth.contentGenerations().length
    auth.logout()
    auth.login(CODEX_AUTH_OTHER_ACCOUNT)
    await sendFailingTurn(context, auth, 'Turn after the account switched.')
    expectContentGenerations(auth, refusedBefore, ['old-1'])

    // Approval replaces the process, loads the new account, and resumes the saved thread.
    await sendRecoveredAnswer(context, auth, {
      stalePid,
      generation: 'new-2',
      prompt: 'Turn under the new account.',
      answer: 'The new account resumed the saved thread.',
      priorPrompt: 'Turn under the first account.',
      priorAnswer: 'The first account answered.',
    })
    expectSameThread(before, await currentNativeAgent(context))
    expect(singleCodexPid(worker.dataDir, wrapper.directory)).not.toBe(stalePid)
    expect(auth.contentGenerations()).toEqual(['old-1', 'old-1', 'new-2'])
  })
})

codexTest('retains the input while signed out and delivers it after the login', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withCodexAuthScenario({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, 'codex-auth-signed-out-', async ({ auth, context, server, wrapper, worker }) => {
    const opened = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-signed-out-' })
    await openWorkspace(page, context.workspaceId)
    await selectAgent(context, opened.agentId)
    await sendNativeAnswer(context, 'Turn before the logout.', 'The turn before the logout completed.')
    const before = await currentNativeAgent(context)
    const stalePid = singleCodexPid(worker.dataDir, wrapper.directory)

    // The logout invalidates the login; the running process's next turn
    // fails and marks the process for replacement.
    const refusedBefore = auth.contentGenerations().length
    auth.logout()
    await sendFailingTurn(context, auth, 'Turn after the logout.')
    expectContentGenerations(auth, refusedBefore, ['old-1'])

    // Approval while signed out replaces the process but cannot deliver the queued input.
    await sendMessage(page, context.modelScript.prompt('Turn retained while signed out.'))
    const retained = await expectRecoveryInput(context)
    expect(isAlive(stalePid), 'the old runtime remains alive until approval').toBe(true)
    const signedOutDialog = await openRecoveryDialog(context)
    await signedOutDialog.getByRole('button', { name: 'Recover login', exact: true }).click()
    await expect(signedOutDialog.getByTestId('input-recovery-result-summary')).toHaveText('Recovered 0 of 1 Codex tab.')
    await expect(signedOutDialog.getByTestId('input-recovery-results')).toContainText('sign-in')
    await signedOutDialog.getByRole('button', { name: 'Done', exact: true }).click()
    expect((await expectRecoveryInput(context)).id).toBe(retained.id)
    expectContentGenerations(auth, refusedBefore, ['old-1'])
    expect(isAlive(stalePid), 'the approved replacement stopped the stale runtime').toBe(false)
    expectSameThread(before, await currentNativeAgent(context))

    // A login does not reuse the earlier approval. Confirm the new physical process explicitly.
    auth.login(CODEX_AUTH_SAME_ACCOUNT)
    const retryBefore = auth.contentGenerations().length
    const step = {
      ...nativeTextStep(context, 'The retained input was delivered after the login.'),
      gate: 'signed-out-retry',
    }
    const stepIndex = await context.modelScript.queue(step)
    await approveRecovery(await openRecoveryDialog(context))
    await context.modelScript.waitForGate(step.gate)
    const retryRequest = await context.modelScript.requestAt(stepIndex)
    const retryText = nativeScenarioModelContextText(context, retryRequest)
    expect(retryText).toContain('Turn before the logout.')
    expect(retryText).toContain('The turn before the logout completed.')
    expect(nativeModelConversationTurns(retryRequest).filter(turn => turn.role === 'user' && turn.text.includes('Turn retained while signed out.'))).toHaveLength(1)
    expectContentGenerations(auth, retryBefore, ['old-2'])
    await context.modelScript.releaseGate(step.gate)
    await context.modelScript.waitForSteps(stepIndex + 1)
    await waitForAgentIdle(page)
    expectSameThread(before, await currentNativeAgent(context))
    expect(codexProcessIds(worker.dataDir, wrapper.directory)).toHaveLength(1)
    expectContentGenerations(auth, retryBefore, ['old-2'])
    expect(auth.contentGenerations()).toEqual(['old-1', 'old-1', 'old-2'])
  })
})

codexTest('recovers one tab while another tab holds an active scripted turn', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withCodexAuthScenario({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, 'codex-auth-tab-a-', async ({ auth, context, server, wrapper, worker }) => {
    await openWorkspace(page, context.workspaceId)
    // Open tab A and complete its turn BEFORE tab B opens: two Codex
    // processes that start together race the first creation of their
    // shared CODEX_HOME state store.
    const tabA = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-tab-a-' })
    await selectAgent(context, tabA.agentId)
    await sendNativeAnswer(context, 'Tab A before the account switched.', 'Tab A completed its first turn.')
    const beforeA = await currentNativeAgent(context)
    const staleA = singleCodexPid(worker.dataDir, wrapper.directory)
    const tabB = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-tab-b-' })
    await selectAgent(context, tabB.agentId)
    const heldStep = nativeTextStep(context, 'Tab B finishes after the release.')
    heldStep.gate = 'codex-auth-tab-b-hold'
    const heldIndex = await context.modelScript.queue(heldStep)
    await sendMessage(page, context.modelScript.prompt('Hold the tab B turn open.'))
    await context.modelScript.waitForGate(heldStep.gate)
    const beforeB = await currentNativeAgent(context)
    const otherPids = codexProcessIds(worker.dataDir, wrapper.directory).filter(pid => pid !== staleA)
    expect(otherPids).toHaveLength(1)
    const pidB = otherPids[0]!
    await selectAgent(context, tabA.agentId)

    const refusedBefore = auth.contentGenerations().length
    auth.login(CODEX_AUTH_OTHER_ACCOUNT)
    await sendFailingTurn(context, auth, 'Tab A turn after the account switched.')
    expectContentGenerations(auth, refusedBefore, ['old-1'])
    const stalePids = codexProcessIds(worker.dataDir, wrapper.directory)
    expect(stalePids).toHaveLength(2)

    // Tab A recovers; tab B's process keeps its held turn.
    await sendRecoveredAnswer(context, auth, {
      stalePid: staleA,
      generation: 'new-2',
      prompt: 'Tab A turn after recovery.',
      answer: 'Tab A recovered while tab B still runs.',
      priorPrompt: 'Tab A before the account switched.',
      priorAnswer: 'Tab A completed its first turn.',
    })
    expectSameThread(beforeA, await currentNativeAgent(context))
    expect((await context.modelScript.status()).pendingGates).toContain(heldStep.gate)
    const recoveredPids = codexProcessIds(worker.dataDir, wrapper.directory)
    expect(recoveredPids).toHaveLength(2)
    expect(recoveredPids).toContain(pidB)
    expect(recoveredPids).not.toContain(staleA)

    // Tab B's turn completes on its untouched process.
    await selectAgent(context, tabB.agentId)
    await context.modelScript.releaseGate('codex-auth-tab-b-hold')
    await context.modelScript.waitForSteps(heldIndex + 1)
    await waitForAgentIdle(page)
    expect(codexProcessIds(worker.dataDir, wrapper.directory)).toEqual(recoveredPids)
    expectSameThread(beforeB, await currentNativeAgent(context))
    expect(auth.contentGenerations()).toEqual(['old-1', 'old-1', 'old-1', 'new-2'])
  })
})

codexTest('recovers several affected tabs independently after each approval', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withCodexAuthScenario({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, 'codex-auth-independent-', async ({ auth, context, server, wrapper, worker }) => {
    await openWorkspace(page, context.workspaceId)
    // As above: the second process opens only after the first state store
    // exists.
    const tabA = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-independent-a-' })
    await selectAgent(context, tabA.agentId)
    await sendNativeAnswer(context, 'Tab A before the switch.', 'Tab A answered first.')
    const beforeA = await currentNativeAgent(context)
    const staleA = singleCodexPid(worker.dataDir, wrapper.directory)
    const tabB = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-independent-b-' })
    await selectAgent(context, tabB.agentId)
    await sendNativeAnswer(context, 'Tab B before the switch.', 'Tab B answered first.')
    const beforeB = await currentNativeAgent(context)
    const stalePids = codexProcessIds(worker.dataDir, wrapper.directory)
    expect(stalePids).toHaveLength(2)
    const otherPids = stalePids.filter(pid => pid !== staleA)
    expect(otherPids).toHaveLength(1)
    const staleB = otherPids[0]!

    const refusedBefore = auth.contentGenerations().length
    auth.login(CODEX_AUTH_OTHER_ACCOUNT)
    await selectAgent(context, tabA.agentId)
    await sendFailingTurn(context, auth, 'Tab A stale turn.')
    expectContentGenerations(auth, refusedBefore, ['old-1'])
    await selectAgent(context, tabB.agentId)
    const refusedBeforeB = auth.contentGenerations().length
    await sendFailingTurn(context, auth, 'Tab B stale turn.')
    expectContentGenerations(auth, refusedBeforeB, ['old-1'])

    // Tab A recovers first; tab B keeps its stale process until its input.
    await selectAgent(context, tabA.agentId)
    await sendRecoveredAnswer(context, auth, {
      stalePid: staleA,
      generation: 'new-2',
      prompt: 'Tab A recovered.',
      answer: 'Tab A recovered first.',
      priorPrompt: 'Tab A before the switch.',
      priorAnswer: 'Tab A answered first.',
    })
    expectSameThread(beforeA, await currentNativeAgent(context))
    const afterA = codexProcessIds(worker.dataDir, wrapper.directory)
    expect(afterA).toHaveLength(2)
    expect(afterA).toContain(staleB)
    expect(afterA).not.toContain(staleA)

    await selectAgent(context, tabB.agentId)
    await sendRecoveredAnswer(context, auth, {
      stalePid: staleB,
      generation: 'new-2',
      prompt: 'Tab B recovered.',
      answer: 'Tab B recovered after its own input.',
      priorPrompt: 'Tab B before the switch.',
      priorAnswer: 'Tab B answered first.',
    })
    expectSameThread(beforeB, await currentNativeAgent(context))
    const afterB = codexProcessIds(worker.dataDir, wrapper.directory)
    expect(afterB).toHaveLength(2)
    expect(afterB).not.toContain(staleA)
    expect(afterB).not.toContain(staleB)
    expect(afterB).toContain(afterA.find(pid => pid !== staleB)!)
    expect(auth.contentGenerations()).toEqual([
      'old-1',
      'old-1',
      'old-1',
      'old-1',
      'new-2',
      'new-2',
    ])
  })
})

codexTest('recovers only the approved batch across workers and requires approval for a later issue', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const fixtures = {
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }
  await withCodexAuthScenario(fixtures, 'codex-auth-batch-a-', async (first) => {
    await withCodexAuthScenario(fixtures, 'codex-auth-batch-b-', async (second) => {
      await openWorkspace(page, fixtures.workspaceId)
      const tabA = await openProviderAgent(first.server, fixtures.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-batch-a-' })
      await selectAgent(first.context, tabA.agentId)
      await sendNativeAnswer(first.context, 'Batch A before the switch.', 'Batch A retained its conversation.')
      const beforeA = await currentNativeAgent(first.context)
      const staleA = singleCodexPid(first.worker.dataDir, first.wrapper.directory)

      const tabB = await openProviderAgent(second.server, fixtures.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-batch-b-' })
      await selectAgent(second.context, tabB.agentId)
      await sendNativeAnswer(second.context, 'Batch B before the switch.', 'Batch B retained its conversation.')
      const beforeB = await currentNativeAgent(second.context)
      const staleB = singleCodexPid(second.worker.dataDir, second.wrapper.directory)

      // A healthy tab shares the first worker but has no observed login failure.
      const healthy = await openProviderAgent(first.server, fixtures.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-batch-healthy-' })
      await selectAgent(first.context, healthy.agentId)
      const heldGate = 'codex-auth-batch-healthy'
      const heldIndex = await modelScript.queue({
        ...nativeTextStep(first.context, 'The healthy held turn completed.'),
        gate: heldGate,
      })
      await sendMessage(page, modelScript.prompt('Keep the healthy batch tab working.'))
      await modelScript.waitForGate(heldGate)
      const beforeHealthy = await currentNativeAgent(first.context)
      const healthyPids = codexProcessIds(first.worker.dataDir, first.wrapper.directory).filter(pid => pid !== staleA)
      expect(healthyPids).toHaveLength(1)
      const healthyPid = healthyPids[0]!

      first.auth.login(CODEX_AUTH_OTHER_ACCOUNT)
      second.auth.login(CODEX_AUTH_OTHER_ACCOUNT)
      await selectAgent(first.context, tabA.agentId)
      await sendFailingTurn(first.context, first.auth, 'Batch A stale turn.')
      await selectAgent(second.context, tabB.agentId)
      await sendFailingTurn(second.context, second.auth, 'Batch B stale turn.')

      const targets = [
        {
          scenario: first,
          agentId: tabA.agentId,
          before: beforeA,
          stalePid: staleA,
          prompt: 'Deliver batch A once.',
          answer: 'Batch A recovered once.',
          priorPrompt: 'Batch A before the switch.',
          priorAnswer: 'Batch A retained its conversation.',
          rule: 'batch-recovery-a',
          gate: 'batch-recovery-a',
        },
        {
          scenario: second,
          agentId: tabB.agentId,
          before: beforeB,
          stalePid: staleB,
          prompt: 'Deliver batch B once.',
          answer: 'Batch B recovered once.',
          priorPrompt: 'Batch B before the switch.',
          priorAnswer: 'Batch B retained its conversation.',
          rule: 'batch-recovery-b',
          gate: 'batch-recovery-b',
        },
      ]
      // Replacements run concurrently, so match their prompts rather than queue order.
      await modelScript.rule(...targets.map(target => ({
        name: target.rule,
        when: { user: target.prompt },
        respond: {
          ...nativeTextStep(target.scenario.context, target.answer),
          gate: target.gate,
        },
        once: true,
      })))
      for (const target of targets) {
        await selectAgent(target.scenario.context, target.agentId)
        await sendMessage(page, modelScript.prompt(target.prompt))
        await expectRecoveryInput(target.scenario.context)
        expect(isAlive(target.stalePid), 'unapproved queued input keeps its old process').toBe(true)
      }
      const healthySnapshot = await readNativeInputQueue(first.server, healthy.agentId)
      expect(healthySnapshot.activeTurn).toBe(true)
      expect(healthySnapshot.processRecovery).toBeUndefined()

      const viewport = page.viewportSize()
      const dialog = await openRecoveryDialog(second.context)
      const batchChoice = dialog.getByRole('checkbox', { name: 'Recover all Codex tabs with login issues' })
      await batchChoice.check()
      await expect(dialog.getByTestId('input-recovery-summary')).toHaveText('2 Codex tabs on 2 workers')
      const list = dialog.getByTestId('input-recovery-targets')
      await expect(list).not.toBeVisible()
      await expect(list.locator('li')).toHaveCount(2)
      const capturedDialog = await dialog.elementHandle()
      if (!capturedDialog)
        throw new Error('The recovery confirmation has no DOM node to retain across the layout change.')
      try {
        // The shell replaces its editor at the mobile breakpoint. The captured
        // confirmation must keep its process identities and selection through that change.
        await page.setViewportSize({ width: 720, height: 480 })
        await expect(page.getByTestId('mobile-swipe-band')).toBeVisible()
        expect(await capturedDialog.evaluate(element => element.isConnected && element === document.querySelector('[data-testid="input-recovery-dialog"]')), 'the same confirmation remains mounted across the layout change').toBe(true)
        await expect(dialog).toBeVisible()
        await expect(batchChoice).toBeChecked()
        await expect(dialog.getByTestId('input-recovery-summary')).toHaveText('2 Codex tabs on 2 workers')
        await expect(list.locator('li')).toHaveCount(2)
        await dialog.getByText('View affected tabs', { exact: true }).click()
        await expect(list.locator('li')).toHaveCount(2)

        await page.setViewportSize({ width: 1440, height: 1100 })
        expect(await capturedDialog.evaluate(element => element.isConnected && element.matches(':modal')), 'the original confirmation stays connected and modal when returning to desktop').toBe(true)
        await page.setViewportSize({ width: 720, height: 480 })
        expect(await capturedDialog.evaluate(element => element.isConnected && element.matches(':modal')), 'the original confirmation stays connected and modal when returning to mobile').toBe(true)

        // Clone only presentation rows, without creating more native processes.
        // Remove them synchronously so no cloned row can enter the approval action.
        const geometry = await list.evaluate((element) => {
          const row = element.firstElementChild
          if (!row)
            throw new Error('The affected-tab list has no row to clone.')
          const clones = Array.from({ length: 128 }, () => row.cloneNode(true))
          try {
            element.append(...clones)
            element.scrollTop = element.scrollHeight
            const dialog = element.closest('[data-testid="input-recovery-dialog"]')
            if (!dialog)
              throw new Error('The affected-tab list has no recovery dialog.')
            const buttons = Array.from(dialog.querySelectorAll('footer button')).map(button => ({
              text: button.textContent,
              top: button.getBoundingClientRect().top,
              bottom: button.getBoundingClientRect().bottom,
            }))
            return {
              height: element.clientHeight,
              scrollHeight: element.scrollHeight,
              scrollTop: element.scrollTop,
              overflow: getComputedStyle(element).overflowY,
              viewportHeight: window.innerHeight,
              buttons,
            }
          }
          finally {
            for (const clone of clones)
              clone.parentNode?.removeChild(clone)
            element.scrollTop = 0
          }
        })
        expect(geometry.overflow).toBe('auto')
        expect(geometry.height).toBeGreaterThan(0)
        expect(geometry.height).toBeLessThan(geometry.viewportHeight / 2)
        expect(geometry.scrollHeight).toBeGreaterThan(geometry.height)
        expect(geometry.scrollTop).toBeGreaterThan(0)
        expect(geometry.buttons.map(button => button.text)).toEqual(['Cancel', 'Recover login'])
        for (const button of geometry.buttons) {
          expect(button.top, `${button.text} stays above the viewport bottom`).toBeGreaterThanOrEqual(0)
          expect(button.bottom, `${button.text} remains visible in the short viewport`).toBeLessThanOrEqual(geometry.viewportHeight)
        }
        await expect(list.locator('li')).toHaveCount(2)
        await approveRecovery(dialog, 2, 2)
      }
      finally {
        await capturedDialog.dispose()
        if (viewport)
          await page.setViewportSize(viewport)
      }
      for (const target of targets)
        await modelScript.waitForGate(target.gate)
      const status = await modelScript.status()
      expect(status.pendingGates).toContain(heldGate)
      expect(isAlive(healthyPid), 'the batch leaves the healthy process alive').toBe(true)
      for (const target of targets) {
        expect(isAlive(target.stalePid), 'approval stopped each affected process').toBe(false)
        expect(status.ruleMatches[target.rule], 'each queued prompt reaches the model exactly once').toBe(1)
        const request = ruleRequest(status, target.rule)
        const text = nativeScenarioModelContextText(target.scenario.context, request)
        expect(text).toContain(target.priorPrompt)
        expect(text).toContain(target.priorAnswer)
        expect(nativeModelConversationTurns(request).filter(turn => turn.role === 'user' && turn.text.includes(target.prompt))).toHaveLength(1)
        await selectAgent(target.scenario.context, target.agentId)
        expectSameThread(target.before, await currentNativeAgent(target.scenario.context))
        await modelScript.releaseGate(target.gate)
        await waitForAgentIdle(page)
        await expect(assistantBubbles(page).filter({ hasText: target.answer }).first()).toBeVisible()
      }
      expect(first.auth.contentGenerations()).toEqual(['old-1', 'old-1', 'old-1', 'new-2'])
      expect(second.auth.contentGenerations()).toEqual(['old-1', 'old-1', 'new-2'])

      // A later issue in the previously healthy tab requires another explicit approval.
      await selectAgent(first.context, healthy.agentId)
      await modelScript.releaseGate(heldGate)
      await modelScript.waitForSteps(heldIndex + 1)
      await waitForAgentIdle(page)
      expectSameThread(beforeHealthy, await currentNativeAgent(first.context))
      expect(isAlive(healthyPid)).toBe(true)
      await sendFailingTurn(first.context, first.auth, 'The healthy tab develops a later login issue.')
      await sendRecoveredAnswer(first.context, first.auth, {
        stalePid: healthyPid,
        generation: 'new-2',
        prompt: 'Recover the later issue after new approval.',
        answer: 'The later issue required its own approval.',
        priorPrompt: 'Keep the healthy batch tab working.',
        priorAnswer: 'The healthy held turn completed.',
      })
      expectSameThread(beforeHealthy, await currentNativeAgent(first.context))
      const completed = await modelScript.status()
      for (const target of targets)
        expect(completed.ruleMatches[target.rule]).toBe(1)
      expect(first.auth.contentGenerations()).toEqual([
        'old-1',
        'old-1',
        'old-1',
        'new-2',
        'old-1',
        'new-2',
      ])
      expect(second.auth.contentGenerations()).toEqual(['old-1', 'old-1', 'new-2'])
    })
  })
})

codexTest('keeps the process through an ordinary permission failure', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withCodexAuthScenario({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, 'codex-auth-permission-', async ({ auth, context, server, wrapper, worker }) => {
    const opened = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-permission-' })
    await openWorkspace(page, context.workspaceId)
    await selectAgent(context, opened.agentId)
    await sendNativeAnswer(context, 'Turn before the permission failure.', 'The healthy turn completed.')
    const before = await currentNativeAgent(context)
    const pid = singleCodexPid(worker.dataDir, wrapper.directory)

    // An ordinary 403 permission failure keeps its existing handling: no
    // credential is involved, so nothing is replaced.
    const generationsBefore = auth.contentGenerations().length
    const failedIndex = await context.modelScript.queue({
      error: { status: 403, message: 'The organization policy refused this request.' },
    })
    await sendMessage(page, context.modelScript.prompt('Turn the policy refuses.'))
    await context.modelScript.waitForSteps(failedIndex + 1)
    await waitForAgentIdle(page)
    expect(singleCodexPid(worker.dataDir, wrapper.directory)).toBe(pid)

    // The same process answers the next turn.
    await sendNativeAnswer(context, 'Turn after the permission failure.', 'The same process answered again.')
    expectContentGenerations(auth, generationsBefore, ['old-1', 'old-1'])
    expect(auth.tokenEndpointCalls()).toBe(0)
    expect(auth.contentGenerations()).toEqual(['old-1', 'old-1', 'old-1'])
    expectSameThread(before, await currentNativeAgent(context))
    expect(singleCodexPid(worker.dataDir, wrapper.directory)).toBe(pid)
  })
})
