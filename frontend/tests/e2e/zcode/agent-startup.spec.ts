import { existsSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { hubSpawnEnv } from '../helpers/server'
import { zcodeTest } from '../zcode-fixtures'
import { zcodeScriptCandidatePaths } from '../zcode-install'

/**
 * The launch that the wrapper holds: the app server.
 *
 * The Worker's ZCode start first runs the same binary as
 * `app-server --stdio --prepare-storage` to locate ZCode's session store
 * (`newZCodeStorageQuery` in providers/zcode/session_store.go), and only then
 * starts the app server. The wrapper accepts one handshake. A held storage query
 * would take it, and the wrapper would then refuse the real app server, which
 * exits with 125. So the storage query runs at once.
 */
const APP_SERVER_LAUNCH = { holdWhen: ['app-server', '--stdio'], passThroughWhen: ['--prepare-storage'] }

zcodeTest.describe('zcode agent startup', () => {
  for (const failed of [false, true]) {
    zcodeTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ native, leapmuxServer }) => {
      // Find each executable on the PATH that the Worker receives, because the Worker starts the executable that it finds there.
      const spawnEnvironment = hubSpawnEnv(leapmuxServer.agentEnv)
      const launcher = findBinary('zcode', spawnEnvironment)
      if (launcher && !process.env.LEAPMUX_ZCODE_SCRIPT) {
        await exerciseAgentStartup(native, {
          launch: { binaryName: 'zcode', executable: launcher, ...APP_SERVER_LAUNCH },
          failed,
          workerEnvironment: () => ({ LEAPMUX_ZCODE_SCRIPT: '', LEAPMUX_ZCODE_NODE: '' }),
        })
        return
      }
      const script = process.env.LEAPMUX_ZCODE_SCRIPT
        ?? zcodeScriptCandidatePaths(process.platform, homedir(), process.env).find(existsSync)
      const executable = findBinary('node', spawnEnvironment)
      if (!script || !existsSync(script) || !executable)
        throw new Error('The controlled ZCode startup requires its actual script and Node interpreter.')
      await exerciseAgentStartup(native, {
        launch: { binaryName: 'node', executable, ...APP_SERVER_LAUNCH },
        failed,
        workerEnvironment: wrapper => ({ LEAPMUX_ZCODE_SCRIPT: script, LEAPMUX_ZCODE_NODE: join(wrapper.directory, process.platform === 'win32' ? 'node.cmd' : 'node') }),
      })
    })
  }
})
