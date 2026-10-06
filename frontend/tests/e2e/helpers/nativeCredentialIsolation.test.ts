import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { assertIsolatedConfiguration, assertPrivateNativePath, exerciseCredentialIsolation } from './nativeCredentialIsolation'

const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
let scratch: string
let runDir: string

beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  scratch = mkdtempSync(join(scratchRoot, 'native-private-path-unit-'))
  runDir = join(scratch, 'private-run')
  mkdirSync(runDir)
})

afterEach(() => rmSync(scratch, { recursive: true, force: true }))

describe('assertPrivateNativePath', () => {
  it('accepts actual private files and directories with spaces', () => {
    const directory = join(runDir, 'native configuration')
    mkdirSync(directory)
    const file = join(directory, 'models.json')
    writeFileSync(file, '{}')
    expect(() => assertPrivateNativePath(directory, runDir)).not.toThrow()
    expect(() => assertPrivateNativePath(file, runDir)).not.toThrow()
  })

  it('refuses a private-looking symlink that resolves outside the native run', () => {
    const outside = join(scratch, 'outside-run')
    mkdirSync(outside)
    const link = join(runDir, 'native-home')
    symlinkSync(outside, link, 'junction')
    expect(() => assertPrivateNativePath(link, runDir)).toThrow('outside the E2E run')
  })

  it('refuses a lexical path outside the private run', () => {
    expect(() => assertPrivateNativePath(scratch, runDir)).toThrow('outside the E2E run')
  })

  it.each([{ path: '', run: 'run' }, { path: 'path', run: '' }])('refuses an empty private path or run directory: %j', ({ path, run }) => {
    expect(() => assertPrivateNativePath(path, run)).toThrow('must be nonempty')
  })
})

describe('assertIsolatedConfiguration', () => {
  const origin = 'http://127.0.0.1:4100'
  const key = 'mock-key-123'

  it('accepts a configuration that points at the mock and states the credential and each marker', () => {
    const configuration = `base_url = "${origin}/v1"\napi_key = "${key}"\nprovider = "mock"`
    expect(() => assertIsolatedConfiguration(configuration, { mockOrigin: origin, expectedCredential: key, configurationMarkers: ['provider = "mock"'] })).not.toThrow()
  })

  it.each([
    ['no mock origin', 'api_key = "mock-key-123"', { expectedCredential: key }, 'does not point at the suite mock'],
    ['no credential', `base_url = "${origin}"`, { expectedCredential: key }, 'does not state the expected credential'],
    ['no marker', `base_url = "${origin}"\napi_key = "${key}"`, { expectedCredential: key, configurationMarkers: ['provider = "mock"'] }, 'lacks the marker'],
  ])('refuses a configuration with %s', (_name, configuration, rules, message) => {
    expect(() => assertIsolatedConfiguration(configuration, { mockOrigin: origin, ...rules })).toThrow(message)
  })

  // A CLI that reads its key from an environment variable keeps no key in the file. The test of such a
  // CLI states that the file holds none, and the accepted mock credential of the turn proves the key.
  it('accepts a configuration that names an environment variable and holds no key', () => {
    const configuration = `base_url = "${origin}"\napi_key_env = "LEAPMUX_E2E_MODEL_API_KEY"`
    expect(() => assertIsolatedConfiguration(configuration, { mockOrigin: origin, absentFromConfiguration: [key], configurationMarkers: ['api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"'] })).not.toThrow()
  })

  it('refuses a configuration that holds a key that it must not hold', () => {
    const configuration = `base_url = "${origin}"\napi_key_env = "LEAPMUX_E2E_MODEL_API_KEY"\napi_key = "${key}"`
    expect(() => assertIsolatedConfiguration(configuration, { mockOrigin: origin, absentFromConfiguration: [key] })).toThrow('holds text that it must not hold')
  })

  it.each([
    ['no statement about the credential', {}, 'must state its credential'],
    ['an empty expected credential', { expectedCredential: '' }, 'must be nonempty'],
    ['an empty text that must be absent', { absentFromConfiguration: [''] }, 'must be nonempty'],
  ])('refuses rules with %s', (_name, rules, message) => {
    expect(() => assertIsolatedConfiguration(`base_url = "${origin}"`, { mockOrigin: origin, ...rules })).toThrow(message)
  })
})

describe('exerciseCredentialIsolation', () => {
  // An unset environment variable reaches the inline configuration as undefined. The check refuses it before it reads
  // the environment, the files, or the browser.
  it.each([[['']], [[undefined]], [['http://127.0.0.1:4100', undefined]]])('refuses the inline configuration %j before any other step', async (inlineConfiguration) => {
    const context = {
      provider: AgentProvider.CODEX,
      workspaceId: 'credential-boundary',
      get leapmuxServer(): never {
        throw new Error('The inline configuration check must run before the environment check.')
      },
      get page(): Page {
        throw new Error('The inline configuration check must run before browser access.')
      },
      get modelScript(): ModelScript {
        throw new Error('The inline configuration check must run before model access.')
      },
    }
    await expect(Reflect.apply(exerciseCredentialIsolation, undefined, [context, { inlineConfiguration, privateDirectories: [runDir], expectedCredential: 'mock-key-123' }])).rejects.toThrow('Each inline native configuration must be a nonempty string.')
  })
})
