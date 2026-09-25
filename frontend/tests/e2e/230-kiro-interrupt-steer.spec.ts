import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { isAlive, listProcesses, newProcessesMatching, withDescendants } from './helpers/processTree'
import { bashToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { closeAgentViaAPI } from './helpers/worktree'
import { expect, KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from './kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const KIRO = AgentProvider.KIRO

/**
 * The text that the command line of each Kiro process holds: the relay
 * `kiro-cli-chat`, and the engine, which runs from Kiro's `kiro-cli` data directory.
 */
const KIRO_PROCESS_TEXT = 'kiro-cli'

/**
 * A shell command that waits until `gate` exists. It gives up after about ten
 * minutes, so a run that stops before it writes the gate leaves no loop behind.
 */
function waitForGateCommand(gate: string): string {
  return `i=0; while [ ! -e '${gate}' ] && [ "$i" -lt 6000 ]; do sleep 0.1; i=$((i+1)); done`
}

/**
 * 230 -- Kiro interrupt, steering and process lifetime.
 *
 * Kiro cancels a turn on the protocol's `session/cancel`, steers a running turn
 * through its own `_session/steer`, and runs as a relay with an engine below it:
 * the worker must stop the whole tree when the agent closes.
 */
kiroTest.describe('Kiro interrupt, steering and process lifetime', () => {
  kiroTest('interrupts a running turn', async ({ page, authenticatedKiroWorkspace, modelScript }) => {
    void authenticatedKiroWorkspace
    // The HOLD keeps the turn running long enough to interrupt it.
    await modelScript.queue({ text: 'A report that never arrives.', delayMs: 60_000 })
    modelScript.allowUnconsumed('the interrupt ends the turn before the held answer arrives')
    await sendMessage(page, modelScript.prompt('Write a long report.'))
    await modelScript.waitForSteps()

    const interrupt = page.locator('[data-testid="interrupt-button"]:visible')
    await expect(interrupt).toBeVisible()
    await interrupt.click()
    await expect(page.locator('[data-testid="result-divider"]:visible').filter({ hasText: /^Turn interrupted$/ })).toBeVisible()
    await waitForAgentIdle(page, 120_000)
  })

  // A steer reaches Kiro while a command runs, and Kiro reads it before its next
  // model call inside the same turn. The command waits for a file that the test
  // writes once the steer left the queue, so the command cannot end before the
  // steer reaches Kiro, however slow the machine is.
  kiroTest('steers a running turn with a queued message', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const gate = join(workingDir, 'steer-gate')

    await modelScript.queue(
      { toolCalls: [bashToolCall(KIRO, 'kiro-slow', waitForGateCommand(gate))] },
      { text: 'STEERED' },
    )
    try {
      await sendMessage(page, modelScript.prompt('Run the slow command, then report.'))
      await modelScript.waitForSteps(1)
      await expect(page.locator('[data-testid="interrupt-button"]:visible')).toBeVisible()

      await sendMessage(page, modelScript.prompt('Reply with the single word STEERED when the command ends.'))
      const queued = page.getByTestId(/^queued-input-/).filter({ hasText: 'Reply with the single word STEERED' })
      await expect(queued).toBeVisible()
      await queued.getByRole('button', { name: 'Steer' }).click()
      // The row leaves the queue once the worker sent the steer to Kiro.
      await expect(queued).toHaveCount(0)
    }
    finally {
      // Also on a failure, so the command of the agent ends.
      writeFileSync(gate, '')
    }

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    // The steer rode the same turn: the request after the command carries it.
    const afterCommand = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(afterCommand?.body)).toContain('Reply with the single word STEERED')
    await expect(assistantBubbles(page).filter({ hasText: 'STEERED' }).first()).toBeVisible()
  })

  kiroTest('stops the whole process tree when the agent closes', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    kiroTest.skip(process.platform === 'win32', 'The process table comes from ps, which Windows does not have')
    const before = new Set(listProcesses().map(row => row.pid))
    const { agentId } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.queue({ text: 'Ready.' })
    await sendMessage(page, modelScript.prompt('Say that you are ready.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // The relay this agent started, and every process below it: the engine and
    // whatever the engine runs.
    const rows = listProcesses()
    const relays = rows.filter(row => !before.has(row.pid) && row.command.includes('kiro-cli-chat')).map(row => row.pid)
    expect(relays.length, 'the agent runs a Kiro relay of its own').toBeGreaterThan(0)
    const tree = withDescendants(rows, relays)
    expect(tree.length, 'the relay runs an engine below it').toBeGreaterThan(relays.length)

    const closed = await closeAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, agentId)
    expect(closed.failureMessage).toBe('')
    await expect.poll(() => tree.filter(isAlive), { message: 'every process of the agent exits' }).toEqual([])
    // The snapshot above misses a process that started after it, and one that the
    // system moved to another parent. The command line still identifies each one.
    await expect.poll(() => newProcessesMatching(listProcesses(), before, KIRO_PROCESS_TEXT).map(row => row.command), { message: 'no Kiro process of the agent stays behind' }).toEqual([])
  })
})
