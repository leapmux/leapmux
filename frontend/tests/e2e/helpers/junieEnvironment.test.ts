import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createJunieEnvironment } from './junieEnvironment'

let runDirectory: string
let previousPath: string | undefined
function options(realHomeDir?: string) {
  return {
    runDirectory,
    homeDir: join(runDirectory, 'home'),
    shimsDirectory: join(runDirectory, 'shims'),
    origin: 'http://127.0.0.1:4567',
    modelKey: 'unit-key',
    modelID: 'unit-model',
    childModel: 'custom:unit-child',
    proxyProvider: 'unit-proxy',
    realHomeDir,
  }
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'junie-environment-test-'))
  mkdirSync(join(runDirectory, 'shims'))
  previousPath = process.env.PATH
  // An empty search path holds no `junie`, so no wrapper is written unless a test puts one there.
  process.env.PATH = join(runDirectory, 'no-binaries')
})

afterEach(() => {
  if (previousPath === undefined)
    delete process.env.PATH
  else
    process.env.PATH = previousPath
  rmSync(runDirectory, { recursive: true, force: true })
})

describe('createJunieEnvironment', () => {
  it('writes both model profiles for the given model, and routes Junie through the private proxy', () => {
    const env = createJunieEnvironment(options())
    const config = JSON.parse(readFileSync(env.JUNIE_CONFIG_LOCATION!, 'utf8'))
    const modelsDir = join(runDirectory, 'junie-models')
    expect(config).toEqual({
      'model-locations': [modelsDir],
      'agent-locations': [join(runDirectory, 'junie-agents')],
      'provider': 'unit-proxy',
      'proxies': [{ 'name': 'unit-proxy', 'kind': 'OpenAI', 'api-url': 'http://127.0.0.1:4567', 'headers': ['Authorization: Bearer unit-key'] }],
    })
    expect(JSON.parse(readFileSync(join(modelsDir, 'mock-model.json'), 'utf8'))).toMatchObject({ id: 'unit-model', baseUrl: 'http://127.0.0.1:4567/v1/chat/completions', apiType: 'OpenAICompletion', apiKey: 'unit-key' })
    expect(JSON.parse(readFileSync(join(modelsDir, 'mock-responses.json'), 'utf8'))).toMatchObject({ id: 'unit-model', baseUrl: 'http://127.0.0.1:4567/v1/responses', apiType: 'OpenAIResponses' })
  })

  it('runs the test subagent on the given child profile', () => {
    createJunieEnvironment(options())
    const child = readFileSync(join(runDirectory, 'junie-agents', 'leapmux-e2e-child.md'), 'utf8')
    expect(child).toContain('name: leapmux-e2e-child\n')
    expect(child).toContain('model: custom:unit-child\n')
  })

  it('keeps the store in the isolated home, skips the update check, and reads no install root without a real home', () => {
    const env = createJunieEnvironment(options())
    expect(env).toEqual({ JUNIE_HOME: join(runDirectory, 'home', '.junie'), JUNIE_CONFIG_LOCATION: join(runDirectory, 'junie-models', 'config.json'), JUNIE_SKIP_UPDATE_CHECK: '1' })
  })

  it('reads the install root itself when the root has no layout to link', () => {
    const realHomeDir = join(runDirectory, 'real-home')
    expect(createJunieEnvironment(options(realHomeDir)).JUNIE_DATA).toBe(join(realHomeDir, '.local', 'share', 'junie'))
  })

  it.runIf(process.platform !== 'win32')('links the installed versions into a private data directory with no staged update', () => {
    const realHomeDir = join(runDirectory, 'real-home')
    const installRoot = join(realHomeDir, '.local', 'share', 'junie')
    const installed = join(installRoot, 'versions', '1.2')
    mkdirSync(installed, { recursive: true })
    symlinkSync(installed, join(installRoot, 'current'))
    mkdirSync(join(installRoot, 'updates'))
    writeFileSync(join(installRoot, 'updates', 'pending-update.json'), '{}\n')
    const data = createJunieEnvironment(options(realHomeDir)).JUNIE_DATA!
    expect(data).toBe(join(runDirectory, 'junie-data'))
    expect(readdirSync(join(data, 'updates'))).toEqual([])
    expect(realpathSync(join(data, 'current'))).toBe(realpathSync(installed))
  })

  it('writes no wrapper when the search path holds no junie', () => {
    createJunieEnvironment(options())
    expect(existsSync(join(runDirectory, 'shims', 'junie'))).toBe(false)
  })

  it.runIf(process.platform !== 'win32')('runs a private copy of the managed launcher script, behind the wrapper directory on PATH', () => {
    const binaries = join(runDirectory, 'binaries')
    mkdirSync(binaries)
    const script = '#!/bin/bash\n# JUNIE_MANAGED_SHIM\nexit 0\n'
    writeFileSync(join(binaries, 'junie'), script, { mode: 0o755 })
    process.env.PATH = binaries
    createJunieEnvironment(options())
    const shims = join(runDirectory, 'shims')
    const wrapper = readFileSync(join(shims, 'junie'), 'utf8')
    expect(wrapper).toContain(`export PATH='${shims}':"$PATH"\n`)
    expect(wrapper).toContain(`exec '${join(shims, 'junie-launcher')}' "$@"`)
    expect(readFileSync(join(shims, 'junie-launcher'), 'utf8')).toBe(script)
  })
})
