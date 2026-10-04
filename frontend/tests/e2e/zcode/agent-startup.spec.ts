import { existsSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from '../acp-fixture-factory'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'
import { zcodeScriptCandidatePaths } from '../zcode-install'

zcodeTest.describe('zcode agent startup', () => {
  zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON ?? '')

  for (const failed of [false, true]) {
    zcodeTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ page, modelScript, leapmuxServer, authenticatedZCodeWorkspace }) => {
      const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
      const launcher = findBinary('zcode')
      if (launcher && !process.env.LEAPMUX_ZCODE_SCRIPT) {
        await exerciseAgentStartup(context, {
          launch: { binaryName: 'zcode', executable: launcher, holdWhen: ['app-server', '--stdio'] },
          failed,
          workerEnvironment: () => ({ LEAPMUX_ZCODE_SCRIPT: '', LEAPMUX_ZCODE_NODE: '' }),
        })
        return
      }
      const script = process.env.LEAPMUX_ZCODE_SCRIPT
        ?? zcodeScriptCandidatePaths(process.platform, homedir(), process.env).find(existsSync)
      const executable = findBinary('node')
      if (!script || !existsSync(script) || !executable)
        throw new Error('The controlled ZCode startup requires its actual script and Node interpreter.')
      await exerciseAgentStartup(context, {
        launch: { binaryName: 'node', executable, holdWhen: ['app-server', '--stdio'] },
        failed,
        workerEnvironment: wrapper => ({ LEAPMUX_ZCODE_SCRIPT: script, LEAPMUX_ZCODE_NODE: join(wrapper.directory, process.platform === 'win32' ? 'node.cmd' : 'node') }),
      })
    })
  }
})
