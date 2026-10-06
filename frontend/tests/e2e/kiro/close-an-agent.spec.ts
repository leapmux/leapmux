import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { listProcesses, withDescendants } from '../helpers/processTree'
import { openWorkspace } from '../helpers/ui'
import { kiroTest, openKiroAgent } from '../kiro-fixtures'
import { kiroEngineProcesses, kiroRunProcesses } from './processOwnership'

kiroTest.describe('kiro process lifetime', () => {
  kiroTest('stops the whole process tree when the agent closes', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const dataDir = leapmuxServer.agentEnv.KIRO_DATA_DIR
    if (!dataDir)
      throw new Error('The Kiro close case requires its private data directory.')
    const beforePids = new Set(listProcesses().map(row => row.pid))
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    let workerPid = 0

    await exerciseCloseAgent({
      page,
      modelScript,
      provider: AgentProvider.KIRO,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    }, {
      nativeOwnership: ({ rows, ownership }) => {
        workerPid = ownership.workerPid
        const owned = new Set(ownership.ownedPids)
        const selected = rows.filter(row => owned.has(row.pid))
        const relays = selected.filter(row => row.command.includes('kiro-cli-chat'))
        expect(relays.length, 'the selected agent runs its own Kiro relay').toBeGreaterThan(0)
        const relayDescendants = new Set(withDescendants(selected, relays.map(row => row.pid)))
        const engines = kiroEngineProcesses(selected, dataDir).filter(row => relayDescendants.has(row.pid))
        expect(engines.length, 'the selected relay runs its own Kiro engine').toBeGreaterThan(0)
      },
    })

    // The private bundle path still identifies an engine after its parent exits.
    await expect.poll(() => kiroRunProcesses(listProcesses(), { workerPid, beforePids, dataDir }).map(row => row.command), {
      message: 'no new relay or private engine of the selected Kiro run stays alive',
    }).toEqual([])
  })
})
