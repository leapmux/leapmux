import type { CodexAuthEnvironment } from '../helpers/codexAuthEnvironment'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { CODEX_AUTH_OTHER_ACCOUNT, CODEX_AUTH_SAME_ACCOUNT, withCodexAuthEnvironment } from '../helpers/codexAuthEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { withNativeStartupWorker } from '../helpers/nativeStartupWorker'
import { listProcesses, withDescendants } from '../helpers/processTree'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { createTestDirectory } from '../helpers/runDirectory'
import { inputQueue, openWorkspace, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'
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
 * startup wrapper's own launcher also names `app-server` in its command, so a
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
async function sendFailingTurn(context: NativeScenarioContext, prompt: string): Promise<void> {
  await sendMessage(context.page, context.modelScript.prompt(prompt))
  await waitForAgentIdle(context.page)
}

/**
 * Require that the failed turn really reached the authentication layer: the
 * fixture refused at least one model request whose credential the running
 * process held. The turn's own text is Codex's error relay, whose wording
 * changes between releases; the refused exchange is the observable fact.
 */
function expectRefusedContent(auth: CodexAuthEnvironment, generationsBefore: number): void {
  expect(auth.contentGenerations().length, 'the stale credential reached the backend and was refused')
    .toBeGreaterThan(generationsBefore)
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
  expect(after.agentSessionId, 'the saved native thread survives').toBe(before.agentSessionId)
}

codexTest('keeps the process through a successful native token refresh', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const initial = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await withCodexAuthEnvironment({
    mockModelURL: leapmuxServer.mockModelUrl!,
    homeDir: createTestDirectory('codex-auth-refresh-'),
  }, async (auth) => {
    await withNativeStartupWorker(initial, passThroughLaunch(initial), {
      workerEnvironment: () => auth.workerEnvironment,
    }, async (workerId, wrapper, worker) => {
      const server = { ...leapmuxServer, workerId }
      const context = await nativeContext({
        page,
        modelScript,
        leapmuxServer: server,
        workspaceId: initial.workspaceId,
      })
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
      expect(auth.contentGenerations().slice(generationsBefore)).toHaveLength(3)
      expect(auth.tokenEndpointCalls()).toBe(1)
      expect(singleCodexPid(worker.dataDir, wrapper.directory)).toBe(pid)
      expectSameThread(before, await currentNativeAgent(context))
    })
  })
})

codexTest('recovers on the next input after the refresh token expires and the same account logs in again', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const initial = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await withCodexAuthEnvironment({
    mockModelURL: leapmuxServer.mockModelUrl!,
    homeDir: createTestDirectory('codex-auth-expired-'),
  }, async (auth) => {
    await withNativeStartupWorker(initial, passThroughLaunch(initial), {
      workerEnvironment: () => auth.workerEnvironment,
    }, async (workerId, wrapper, worker) => {
      const server = { ...leapmuxServer, workerId }
      const context = await nativeContext({
        page,
        modelScript,
        leapmuxServer: server,
        workspaceId: initial.workspaceId,
      })
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
      await sendFailingTurn(context, 'Turn whose login cannot refresh.')
      expectRefusedContent(auth, refusedBefore)

      // A fresh login of the same account exists; the NEXT input replaces the
      // process, resumes the saved thread, and delivers once.
      auth.login(CODEX_AUTH_SAME_ACCOUNT)
      await sendNativeAnswer(context, 'Turn after signing in again.', 'The replaced process resumed the saved thread.')
      expectSameThread(before, await currentNativeAgent(context))
      expect(singleCodexPid(worker.dataDir, wrapper.directory)).not.toBe(stalePid)
    })
  })
})

codexTest('recovers on the next input after a logout and login with a different account', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const initial = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await withCodexAuthEnvironment({
    mockModelURL: leapmuxServer.mockModelUrl!,
    homeDir: createTestDirectory('codex-auth-switch-'),
  }, async (auth) => {
    await withNativeStartupWorker(initial, passThroughLaunch(initial), {
      workerEnvironment: () => auth.workerEnvironment,
    }, async (workerId, wrapper, worker) => {
      const server = { ...leapmuxServer, workerId }
      const context = await nativeContext({
        page,
        modelScript,
        leapmuxServer: server,
        workspaceId: initial.workspaceId,
      })
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
      await sendFailingTurn(context, 'Turn after the account switched.')
      expectRefusedContent(auth, refusedBefore)

      // The next input replaces the process; the replacement loads the new
      // account and resumes the saved thread.
      await sendNativeAnswer(context, 'Turn under the new account.', 'The new account resumed the saved thread.')
      expectSameThread(before, await currentNativeAgent(context))
      expect(singleCodexPid(worker.dataDir, wrapper.directory)).not.toBe(stalePid)
      expect(auth.contentGenerations().at(-1)).toMatch(/^new-\d+$/)
    })
  })
})

codexTest('retains the input while signed out and delivers it after the login', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const initial = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await withCodexAuthEnvironment({
    mockModelURL: leapmuxServer.mockModelUrl!,
    homeDir: createTestDirectory('codex-auth-signed-out-'),
  }, async (auth) => {
    await withNativeStartupWorker(initial, passThroughLaunch(initial), {
      workerEnvironment: () => auth.workerEnvironment,
    }, async (workerId, wrapper, worker) => {
      const server = { ...leapmuxServer, workerId }
      const context = await nativeContext({
        page,
        modelScript,
        leapmuxServer: server,
        workspaceId: initial.workspaceId,
      })
      const opened = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-signed-out-' })
      await openWorkspace(page, context.workspaceId)
      await selectAgent(context, opened.agentId)
      await sendNativeAnswer(context, 'Turn before the logout.', 'The turn before the logout completed.')
      const before = await currentNativeAgent(context)

      // The logout invalidates the login; the running process's next turn
      // fails and marks the process for replacement.
      const refusedBefore = auth.contentGenerations().length
      auth.logout()
      await sendFailingTurn(context, 'Turn after the logout.')
      expectRefusedContent(auth, refusedBefore)

      // The next input replaces the process, whose fresh state is still
      // signed out: the input is retained as a failed, retryable item, and
      // the thread survives. (The queue is not paused: nothing transient
      // holds it, only the missing login.)
      await sendMessage(page, context.modelScript.prompt('Turn retained while signed out.'))
      const failed = await expectRetainedInput(context, 'sign-in')
      expectSameThread(before, await currentNativeAgent(context))

      // The login returns; the retained input retries into the replacement
      // and is delivered once.
      auth.login(CODEX_AUTH_SAME_ACCOUNT)
      const step = nativeTextStep(context, 'The retained input was delivered after the login.')
      const stepIndex = await context.modelScript.queue(step)
      await inputQueue(page).getByTestId(`queued-input-${failed.id}`).getByRole('button', { name: 'Retry', exact: true }).click()
      await context.modelScript.waitForSteps(stepIndex + 1)
      await waitForAgentIdle(page)
      expectSameThread(before, await currentNativeAgent(context))
      expect(codexProcessIds(worker.dataDir, wrapper.directory)).toHaveLength(1)
    })
  })
})

codexTest('recovers one tab while another tab holds an active scripted turn', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const initial = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await withCodexAuthEnvironment({
    mockModelURL: leapmuxServer.mockModelUrl!,
    homeDir: createTestDirectory('codex-auth-tab-a-'),
  }, async (auth) => {
    await withNativeStartupWorker(initial, passThroughLaunch(initial), {
      workerEnvironment: () => auth.workerEnvironment,
    }, async (workerId, wrapper, worker) => {
      const server = { ...leapmuxServer, workerId }
      const context = await nativeContext({
        page,
        modelScript,
        leapmuxServer: server,
        workspaceId: initial.workspaceId,
      })
      await openWorkspace(page, context.workspaceId)
      // Open tab A and complete its turn BEFORE tab B opens: two Codex
      // processes that start together race the first creation of their
      // shared CODEX_HOME state store.
      const tabA = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-tab-a-' })
      await selectAgent(context, tabA.agentId)
      await sendNativeAnswer(context, 'Tab A before the account switched.', 'Tab A completed its first turn.')
      const tabB = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-tab-b-' })
      await selectAgent(context, tabB.agentId)
      const heldStep = nativeTextStep(context, 'Tab B finishes after the release.')
      heldStep.gate = 'codex-auth-tab-b-hold'
      const heldIndex = await context.modelScript.queue(heldStep)
      await sendMessage(page, context.modelScript.prompt('Hold the tab B turn open.'))
      await selectAgent(context, tabA.agentId)

      const refusedBefore = auth.contentGenerations().length
      auth.login(CODEX_AUTH_OTHER_ACCOUNT)
      await sendFailingTurn(context, 'Tab A turn after the account switched.')
      expectRefusedContent(auth, refusedBefore)
      const stalePids = codexProcessIds(worker.dataDir, wrapper.directory)
      expect(stalePids).toHaveLength(2)

      // Tab A recovers; tab B's process keeps its held turn.
      await sendNativeAnswer(context, 'Tab A turn after recovery.', 'Tab A recovered while tab B still runs.')
      const recoveredPids = codexProcessIds(worker.dataDir, wrapper.directory)
      expect(recoveredPids).toHaveLength(2)
      expect(recoveredPids).toContain(stalePids[1]!)

      // Tab B's turn completes on its untouched process.
      await selectAgent(context, tabB.agentId)
      await context.modelScript.releaseGate('codex-auth-tab-b-hold')
      await context.modelScript.waitForSteps(heldIndex + 1)
      await waitForAgentIdle(page)
      expect(codexProcessIds(worker.dataDir, wrapper.directory)).toEqual(recoveredPids)
    })
  })
})

codexTest('recovers several affected tabs independently as each receives input', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const initial = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await withCodexAuthEnvironment({
    mockModelURL: leapmuxServer.mockModelUrl!,
    homeDir: createTestDirectory('codex-auth-independent-'),
  }, async (auth) => {
    await withNativeStartupWorker(initial, passThroughLaunch(initial), {
      workerEnvironment: () => auth.workerEnvironment,
    }, async (workerId, wrapper, worker) => {
      const server = { ...leapmuxServer, workerId }
      const context = await nativeContext({
        page,
        modelScript,
        leapmuxServer: server,
        workspaceId: initial.workspaceId,
      })
      await openWorkspace(page, context.workspaceId)
      // As above: the second process opens only after the first state store
      // exists.
      const tabA = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-independent-a-' })
      await selectAgent(context, tabA.agentId)
      await sendNativeAnswer(context, 'Tab A before the switch.', 'Tab A answered first.')
      const tabB = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-independent-b-' })
      await selectAgent(context, tabB.agentId)
      await sendNativeAnswer(context, 'Tab B before the switch.', 'Tab B answered first.')
      const stalePids = codexProcessIds(worker.dataDir, wrapper.directory)
      expect(stalePids).toHaveLength(2)

      const refusedBefore = auth.contentGenerations().length
      auth.login(CODEX_AUTH_OTHER_ACCOUNT)
      await selectAgent(context, tabA.agentId)
      await sendFailingTurn(context, 'Tab A stale turn.')
      expectRefusedContent(auth, refusedBefore)
      await selectAgent(context, tabB.agentId)
      await sendFailingTurn(context, 'Tab B stale turn.')
      expectRefusedContent(auth, refusedBefore)

      // Tab A recovers first; tab B keeps its stale process until its input.
      await selectAgent(context, tabA.agentId)
      await sendNativeAnswer(context, 'Tab A recovered.', 'Tab A recovered first.')
      const afterA = codexProcessIds(worker.dataDir, wrapper.directory)
      expect(afterA).toHaveLength(2)
      expect(afterA).toContain(stalePids[1]!)

      await selectAgent(context, tabB.agentId)
      await sendNativeAnswer(context, 'Tab B recovered.', 'Tab B recovered after its own input.')
      const afterB = codexProcessIds(worker.dataDir, wrapper.directory)
      expect(afterB).toHaveLength(2)
      expect(afterB).not.toContain(stalePids[0]!)
      expect(afterB).not.toContain(stalePids[1]!)
    })
  })
})

codexTest('keeps the process through an ordinary permission failure', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const initial = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await withCodexAuthEnvironment({
    mockModelURL: leapmuxServer.mockModelUrl!,
    homeDir: createTestDirectory('codex-auth-permission-'),
  }, async (auth) => {
    await withNativeStartupWorker(initial, passThroughLaunch(initial), {
      workerEnvironment: () => auth.workerEnvironment,
    }, async (workerId, wrapper, worker) => {
      const server = { ...leapmuxServer, workerId }
      const context = await nativeContext({
        page,
        modelScript,
        leapmuxServer: server,
        workspaceId: initial.workspaceId,
      })
      const opened = await openProviderAgent(server, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-auth-permission-' })
      await openWorkspace(page, context.workspaceId)
      await selectAgent(context, opened.agentId)
      await sendNativeAnswer(context, 'Turn before the permission failure.', 'The healthy turn completed.')
      const before = await currentNativeAgent(context)
      const pid = singleCodexPid(worker.dataDir, wrapper.directory)

      // An ordinary 403 permission failure keeps its existing handling: no
      // credential is involved, so nothing is replaced.
      const failedIndex = await context.modelScript.queue({
        error: { status: 403, message: 'The organization policy refused this request.' },
      })
      await sendMessage(page, context.modelScript.prompt('Turn the policy refuses.'))
      await context.modelScript.waitForSteps(failedIndex + 1)
      await waitForAgentIdle(page)
      expect(singleCodexPid(worker.dataDir, wrapper.directory)).toBe(pid)

      // The same process answers the next turn.
      await sendNativeAnswer(context, 'Turn after the permission failure.', 'The same process answered again.')
      expectSameThread(before, await currentNativeAgent(context))
      expect(singleCodexPid(worker.dataDir, wrapper.directory)).toBe(pid)
    })
  })
})
