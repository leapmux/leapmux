import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { mcpProbeServer } from './mcpProbeServer'
import { createPiEnvironment, piAgentDirectory } from './piEnvironment'

let runDirectory: string
let homeDir: string
let shimsDirectory: string
let previousPath: string | undefined
const echoServer = mcpProbeServer('echo_probe', '/srv/echo.mjs')
const options = (realHomeDir?: string) => ({ homeDir, shimsDirectory, baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model', flashModelID: 'unit-flash', plainModelID: 'unit-plain', mcpEchoServer: echoServer, realHomeDir })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'pi-environment-test-'))
  homeDir = join(runDirectory, 'home')
  shimsDirectory = join(runDirectory, 'shims')
  mkdirSync(homeDir)
  mkdirSync(shimsDirectory)
  previousPath = process.env.PATH
  // An empty search path holds no `pi`, so no launch wrapper is written unless a test puts one there.
  process.env.PATH = join(runDirectory, 'no-binaries')
})

afterEach(() => {
  if (previousPath === undefined)
    delete process.env.PATH
  else
    process.env.PATH = previousPath
  rmSync(runDirectory, { recursive: true, force: true })
})

/** Put a `pi` on the search path that records its arguments, one to a line, and return the file of the record. */
function installRecordingPi(): string {
  const binaries = join(runDirectory, 'binaries')
  mkdirSync(binaries)
  const record = join(runDirectory, 'started')
  writeFileSync(join(binaries, 'pi'), `#!/bin/sh\nprintf '%s\\n' "$@" > ${JSON.stringify(record)}\n`, { mode: 0o755 })
  process.env.PATH = binaries
  return record
}

/** Run the launch wrapper with `args`, and return the arguments that the installed `pi` received. */
function launch(args: string[], record: string): string[] {
  execFileSync(join(shimsDirectory, 'pi'), args)
  return readFileSync(record, 'utf8').split('\n').slice(0, -1)
}

describe('createPiEnvironment', () => {
  it('writes a provider of the three models: two reason, and the second of those takes an effort', () => {
    const env = createPiEnvironment(options())
    expect(env.PI_CODING_AGENT_DIR).toBe(piAgentDirectory(homeDir))
    const models = JSON.parse(readFileSync(join(env.PI_CODING_AGENT_DIR!, 'models.json'), 'utf8'))
    expect(models.providers.zai).toMatchObject({ baseUrl: 'http://127.0.0.1:4567/v1', api: 'openai-completions', apiKey: 'unit-key' })
    expect(models.providers.zai.models.map((model: { id: string }) => model.id)).toEqual(['unit-model', 'unit-flash', 'unit-plain'])
    expect(models.providers.zai.models.map((model: { reasoning: boolean }) => model.reasoning)).toEqual([true, true, false])
    expect(models.providers.zai.models[0].compat).toBeUndefined()
    expect(models.providers.zai.models[1].compat).toEqual({ supportsReasoningEffort: true })
    expect(models.providers.zai.models[2].compat).toBeUndefined()
  })

  it('selects the default model and states no package without a real home', () => {
    const env = createPiEnvironment(options())
    expect(JSON.parse(readFileSync(join(env.PI_CODING_AGENT_DIR!, 'settings.json'), 'utf8'))).toEqual({
      defaultProvider: 'zai',
      defaultModel: 'unit-model',
      compaction: { keepRecentTokens: 32 },
      packages: [],
    })
  })

  it('loads the packages of the real home', () => {
    const realHomeDir = join(homeDir, 'real-home')
    const settings = JSON.parse(readFileSync(join(createPiEnvironment(options(realHomeDir)).PI_CODING_AGENT_DIR!, 'settings.json'), 'utf8'))
    expect(settings.packages).toContain(join(realHomeDir, '.pi', 'agent', 'npm', 'node_modules', 'pi-goal-x'))
    expect(settings.packages.every((path: string) => path.startsWith(join(realHomeDir, '.pi', 'agent', 'npm', 'node_modules')))).toBe(true)
  })

  it('exposes the echo server directly, and turns off every management request', () => {
    const env = createPiEnvironment(options())
    expect(JSON.parse(readFileSync(join(env.PI_CODING_AGENT_DIR!, 'mcp.json'), 'utf8'))).toEqual({
      mcpServers: { echo_probe: { command: echoServer.command, args: ['/srv/echo.mjs'], exposure: 'direct' } },
    })
    expect(env).toMatchObject({ PI_OFFLINE: '1', PI_SKIP_VERSION_CHECK: '1', PI_CODING_AGENT_SESSION_DIR: '' })
  })

  it('writes no launch wrapper when the search path holds no pi', () => {
    createPiEnvironment(options())
    expect(existsSync(join(shimsDirectory, 'pi'))).toBe(false)
  })

  // Pi reads AGENTS.md and its relatives from each directory above its working directory, up to the root of the file
  // system, and only the flag turns that off.
  it.runIf(process.platform !== 'win32')('writes a launch wrapper that starts each session of the installed pi without context files', () => {
    const record = installRecordingPi()
    createPiEnvironment(options())
    expect(launch(['--mode', 'rpc', '--session', 'unit-session'], record)).toEqual(['--no-context-files', '--mode', 'rpc', '--session', 'unit-session'])
    expect(launch([], record)).toEqual(['--no-context-files'])
  })

  // A subcommand refuses an option that it does not know, so the wrapper adds none to it.
  it.runIf(process.platform !== 'win32')('passes a subcommand to the installed pi unchanged', () => {
    const record = installRecordingPi()
    createPiEnvironment(options())
    expect(launch(['install', 'npm:unit-package'], record)).toEqual(['install', 'npm:unit-package'])
    expect(launch(['config', '--json'], record)).toEqual(['config', '--json'])
  })

  // The wrapper directory is first on the PATH of each agent. A wrapper that found itself would start itself again,
  // forever.
  it.runIf(process.platform !== 'win32')('refuses to wrap a pi in the wrapper directory itself', () => {
    writeFileSync(join(shimsDirectory, 'pi'), '#!/bin/sh\n', { mode: 0o755 })
    process.env.PATH = shimsDirectory
    expect(() => createPiEnvironment(options())).toThrow(`The pi on PATH (${join(shimsDirectory, 'pi')}) is the private wrapper itself`)
  })
})
