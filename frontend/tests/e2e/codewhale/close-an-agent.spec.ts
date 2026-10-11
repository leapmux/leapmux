import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { configuredMcpScript, MCP_ECHO_SERVER_NAME } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

codewhaleTest('closes the UI tab and waits for owned process exit and Worker close', async ({ native, leapmuxServer }) => {
  const home = leapmuxServer.agentEnv.CODEWHALE_HOME
  if (!home)
    throw new Error('The Codewhale close test requires its isolated native home.')
  const mcpScript = configuredMcpScript(join(home, 'mcp.json'), 'servers', MCP_ECHO_SERVER_NAME)
  await exerciseCloseAgent(native, {
    // Codewhale starts a configured MCP server only for the first call of one of its tools,
    // and the server then runs as a child of the runtime until the runtime ends. A runtime
    // that no MCP call reached has no MCP process to own, so one echo call comes first.
    prepare: () => exerciseMcpEcho(native, 'codewhale-close'),
    nativeOwnership: ({ rows, ownership }) => {
      const owned = rows.filter(row => ownership.ownedPids.includes(row.pid))
      const runtime = owned.find((row) => {
        const command = row.rawCommand ?? row.command
        const quote = command[0] === '"' || command[0] === '\'' ? command[0] : undefined
        const end = quote ? command.indexOf(quote, 1) : command.search(/\s/)
        if (quote && end < 0)
          return false
        const executable = row.executable ?? command.slice(quote ? 1 : 0, end < 0 ? undefined : end)
        const executableName = basename(executable.replaceAll('\\', '/')).toLowerCase()
        const words = command.split(/\s+/).map(word => word.replaceAll('"', '').replaceAll('\'', ''))
        return ['codewhale', 'codewhale.exe'].includes(executableName)
          && words.includes('app-server') && words.includes('--http')
      })
      expect(runtime, 'the Worker owns the downloaded native Codewhale runtime').toBeDefined()
      expect((runtime?.rawCommand ?? runtime?.command ?? '').includes('wrapper.js')).toBe(false)
      expect(owned.some(row => (row.rawCommand ?? row.command).includes(mcpScript)), 'the native runtime owns its configured MCP process').toBe(true)
    },
  })
})
