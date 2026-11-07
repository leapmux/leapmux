import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, mkdirSync, mkdtempSync, rmSync, symlinkSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { antigravityNativeSessionDir, antigravityNativeSessionPath } from './nativeStore'

const state = vi.hoisted(() => ({ runDir: '' }))
vi.mock('../helpers/server', () => ({
  getGlobalState: () => ({ tmpDir: state.runDir }),
}))

let scratch: string
beforeEach(() => {
  const root = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(root, { recursive: true })
  scratch = mkdtempSync(join(root, 'antigravity-native-store-'))
  state.runDir = join(scratch, 'private-run')
  mkdirSync(state.runDir)
})
afterEach(() => rmSync(scratch, { recursive: true, force: true }))

function context(home: string | undefined): ManagedNativeScenarioContext {
  return {
    provider: AgentProvider.ANTIGRAVITY_CLI,
    providerAgent: { provider: AgentProvider.ANTIGRAVITY_CLI, prefix: 'native-store-unit' },
    workspaceId: 'native-store-workspace',
    leapmuxServer: {
      hubUrl: 'http://unused.invalid',
      adminToken: 'unused',
      workerId: 'native-store-worker',
      agentEnv: home === undefined
        ? {}
        : { HOME: home },
    },
    get page(): never { throw new Error('The native path helper must not access a page.') },
    get modelScript(): never { throw new Error('The native path helper must not access a model.') },
  }
}

describe('antigravityNativeSessionPath', () => {
  it('constructs the exact session path without creating or reading its directory', () => {
    const home = join(state.runDir, 'isolated home')
    const agent = create(AgentInfoSchema, { agentSessionId: 'native-session' })
    const expected = join(home, '.gemini', 'antigravity-cli', 'brain', 'native-session')
    expect(antigravityNativeSessionPath(context(home), agent)).toBe(expected)
    expect(existsSync(home)).toBe(false)
  })

  it.each([undefined, ''])('rejects a missing or empty private home: %j', (home) => {
    expect(() => antigravityNativeSessionPath(context(home), create(AgentInfoSchema, { agentSessionId: 'session' }))).toThrow('no private home or identity')
  })

  it('rejects an absent native session identity', () => {
    expect(() => antigravityNativeSessionPath(context(state.runDir), create(AgentInfoSchema))).toThrow('no private home or identity')
  })
})

describe('antigravityNativeSessionDir', () => {
  it('requires the directory to exist and returns the same constructed path', () => {
    const native = context(join(state.runDir, 'home'))
    const agent = create(AgentInfoSchema, { agentSessionId: 'session' })
    const path = antigravityNativeSessionPath(native, agent)
    expect(() => antigravityNativeSessionDir(native, agent)).toThrow('does not exist')
    mkdirSync(path, { recursive: true })
    expect(antigravityNativeSessionDir(native, agent)).toBe(path)
  })

  it('rejects a session directory that resolves outside its private run', () => {
    const native = context(join(state.runDir, 'home'))
    const agent = create(AgentInfoSchema, { agentSessionId: 'session' })
    const path = antigravityNativeSessionPath(native, agent)
    const outside = join(scratch, 'outside')
    mkdirSync(dirname(path), { recursive: true })
    mkdirSync(outside)
    symlinkSync(outside, path, 'junction')
    expect(() => antigravityNativeSessionDir(native, agent)).toThrow('outside the E2E run')
  })
})
