import {
  deregisterWorkerViaAPI,
  listOnlineWorkerIDsViaAPI,
  mintRegistrationKeyViaAPI,
  waitForNewOnlineWorkerViaAPI,
} from './api'
import { withCleanup } from './cleanup'
import { stopProcess } from './process'
import { spawnTestProcess } from './processRegistry'
import { createTestDirectory } from './runDirectory'
import { getGlobalState, hubSpawnEnv } from './server'
import { createServerOutput } from './serverOutput'

interface SubscriberWorkerServer {
  hubUrl: string
  adminToken: string
  agentEnv: Record<string, string>
}

/** Run a Claude subscriber against the shared mock endpoint on a private Worker. */
export async function withClaudeSubscriberWorker(
  server: SubscriberWorkerServer,
  use: (workerId: string) => Promise<void>,
): Promise<void> {
  const token = server.agentEnv.LEAPMUX_E2E_MODEL_API_KEY
  if (!token)
    throw new Error('The mock Claude subscriber needs the isolated model token')

  const registrationKey = await mintRegistrationKeyViaAPI(server.hubUrl, server.adminToken)
  const previousWorkers = new Set(await listOnlineWorkerIDsViaAPI(server.hubUrl, server.adminToken))
  const output = createServerOutput()
  const outputStart = output.mark()
  const worker = spawnTestProcess(getGlobalState().binaryPath, [
    'worker',
    '--hub',
    server.hubUrl,
    '--registration-key',
    registrationKey,
    '--data-dir',
    createTestDirectory('claude-subscriber-worker-'),
  ], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: hubSpawnEnv({
      ...server.agentEnv,
      LEAPMUX_WORKER_NAME: 'Claude subscriber test',
      ANTHROPIC_API_KEY: undefined,
      ANTHROPIC_AUTH_TOKEN: undefined,
      CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR: undefined,
      CLAUDE_CODE_USE_BEDROCK: undefined,
      CLAUDE_CODE_USE_VERTEX: undefined,
      CLAUDE_CODE_USE_FOUNDRY: undefined,
      CLAUDE_CODE_OAUTH_TOKEN: token,
    }),
  })
  output.capture(worker, 'claude-subscriber-worker')
  let workerId: string | undefined
  try {
    await withCleanup(async () => {
      workerId = await waitForNewOnlineWorkerViaAPI(server.hubUrl, server.adminToken, previousWorkers)
      await use(workerId)
    }, async () => {
      await stopProcess(worker)
      if (workerId)
        await deregisterWorkerViaAPI(server.hubUrl, server.adminToken, workerId)
    })
  }
  catch (error) {
    const recentOutput = output.since(outputStart).split('\n').slice(-80).join('\n')
    throw new Error(`Claude subscriber Worker failed.\n${recentOutput}`, { cause: error })
  }
}
