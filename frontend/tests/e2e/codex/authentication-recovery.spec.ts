import type { CodexAuthEnvironment } from '../helpers/codexAuthEnvironment'
import type { ManagedNativeScenarioContext, NativeContextFixtures, NativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeStartupLaunch, NativeStartupWrapper } from '../helpers/nativeStartupWrapper'
import type { NativeWorker } from '../helpers/nativeWorker'
import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { CODEX_AUTH_OTHER_ACCOUNT, CODEX_AUTH_SAME_ACCOUNT, withCodexAuthEnvironment } from '../helpers/codexAuthEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent, nativeModelConversationTurns, nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { withNativeStartupWorker } from '../helpers/nativeStartupWorker'
import { isAlive, listProcesses, withDescendants } from '../helpers/processTree'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { createTestDirectory } from '../helpers/runDirectory'
import { assistantBubbles, inputQueue, openWorkspace, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { CODEX_AGENT, nativeContext, nativeLaunch } from './scenarios'

/**
 * Authentication recovery with the real Codex CLI: an expired or replaced
 * ChatGPT login recovers on the NEXT input, by replacing the affected process
 * through the ordinary start path, resuming the saved thread, and delivering
 * the message once. Native refreshes that succeed keep the process.
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
}

/**
 * Require that the selected tab retains its one input as a failed, retryable
 * item whose stated reason contains `reason`, and return that item.
 */
async function expectRetainedInput(context: NativeScenarioContext & { leapmuxServer: Parameters<typeof readNativeInputQueue>[0] }, reason: string) {
  const agent = await currentNativeAgent(context)
  const item = await retryUntilPass(async () => {
    const snapshot = await readNativeInputQueue(context.leapmuxServer, agent.id)
    const failed = snapshot.items.find(candidate =>
      candidate.state === AgentInputState.FAILED && candidate.error.includes(reason))
    expect(failed, 'the retained input stays failed with its reason').toBeDefined()
    return failed!
  })
  await expect(inputQueue(context.page).getByTestId(`queued-input-${item.id}`)).toContainText(reason)
  return item
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

codexTest('recovers on the next input after the refresh token expires and the same account logs in again', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
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

    // A fresh login of the same account exists; the NEXT input replaces the
    // process, resumes the saved thread, and delivers once.
    auth.login(CODEX_AUTH_SAME_ACCOUNT)
    await sendRecoveredAnswer(context, auth, {
      stalePid,
      generation: 'old-2',
      prompt: 'Turn after signing in again.',
      answer: 'The replaced process resumed the saved thread.',
      priorPrompt: 'Turn before the login expired.',
      priorAnswer: 'The healthy turn completed.',
    })
    expectSameThread(before, await currentNativeAgent(context))
    expect(singleCodexPid(worker.dataDir, wrapper.directory)).not.toBe(stalePid)
    expect(auth.contentGenerations()).toEqual(['old-1', 'old-1', 'expired', 'old-2'])
  })
})

codexTest('recovers on the next input after a logout and login with a different account', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
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

    // The next input replaces the process; the replacement loads the new
    // account and resumes the saved thread.
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

    // The next input replaces the process, whose fresh state is still
    // signed out: the input is retained as a failed, retryable item, and
    // the thread survives. (The queue is not paused: nothing transient
    // holds it, only the missing login.)
    await sendMessage(page, context.modelScript.prompt('Turn retained while signed out.'))
    const failed = await expectRetainedInput(context, 'sign-in')
    expectContentGenerations(auth, refusedBefore, ['old-1'])
    expect(isAlive(stalePid), 'the signed-out replacement stopped the stale runtime').toBe(false)
    expectSameThread(before, await currentNativeAgent(context))

    // The login returns; the retained input retries into the replacement
    // and is delivered once.
    auth.login(CODEX_AUTH_SAME_ACCOUNT)
    const retryBefore = auth.contentGenerations().length
    const step = {
      ...nativeTextStep(context, 'The retained input was delivered after the login.'),
      gate: 'signed-out-retry',
    }
    const stepIndex = await context.modelScript.queue(step)
    await inputQueue(page).getByTestId(`queued-input-${failed.id}`).getByRole('button', { name: 'Retry', exact: true }).click()
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

codexTest('recovers several affected tabs independently as each receives input', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
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
