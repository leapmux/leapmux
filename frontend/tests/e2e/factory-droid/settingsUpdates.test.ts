import type { MessageInitShape } from '@bufbuild/protobuf'
import type { Page } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { DroidNativeSettingsSnapshot } from './settingsSnapshot'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { startWaitLimitForTests } from '../helpers/testDeadline'
import { droidSettingsMatch, expectDroidNativeSettings, readDroidNativeSettings } from './settingsUpdates'

const worker = vi.hoisted(() => ({
  owner: undefined as AgentInfo | undefined,
  reads: [] as (AgentInfo | null | Error)[],
  failure: undefined as Error | undefined,
  agentIds: [] as string[],
  onRead: undefined as (() => void) | undefined,
}))

vi.mock('../helpers/nativeMessages', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/nativeMessages')>(),
  readAllAgentMessages: async () => { throw new Error('The Worker stores no native settings frames.') },
}))

vi.mock('../helpers/nativeScenario', () => ({
  selectedAgentTabId: async () => 'droid-agent',
  nativeAgentById: async (_context: unknown, agentId: string) => {
    worker.agentIds.push(agentId)
    worker.onRead?.()
    if (worker.failure)
      throw worker.failure
    const value = worker.reads.length
      ? worker.reads.shift()
      : worker.owner
    if (value instanceof Error)
      throw value
    return value ?? null
  },
}))

let factoryHome = ''
let endWaitLimit: (() => void) | undefined
const context = {
  page: {} as Page,
  leapmuxServer: {
    hubUrl: 'http://unused.invalid',
    adminToken: 'unused',
    workerId: 'unused',
    agentEnv: { FACTORY_HOME_OVERRIDE: '' },
  },
}

function nativeOwner(fields: MessageInitShape<typeof AgentInfoSchema> = {}): AgentInfo {
  return create(AgentInfoSchema, {
    id: 'droid-agent',
    workerId: 'unused',
    agentProvider: AgentProvider.DROID,
    status: AgentStatus.ACTIVE,
    agentSessionId: 'session-1',
    workingDir: '/native/work',
    ...fields,
  })
}

function snapshot(fields: Partial<DroidNativeSettingsSnapshot>): DroidNativeSettingsSnapshot {
  return {
    modelId: undefined,
    reasoningEffort: undefined,
    interactionMode: undefined,
    autonomyLevel: undefined,
    ...fields,
  }
}

function writeSettings(value: unknown, session = 'session-1', directoryKey = '-native-work'): string {
  const directory = join(factoryHome, '.factory', 'sessions', directoryKey)
  mkdirSync(directory, { recursive: true })
  const path = join(directory, `${session}.settings.json`)
  writeFileSync(path, JSON.stringify(value))
  return path
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  factoryHome = mkdtempSync(join(scratch, 'droid-native-settings-'))
  context.leapmuxServer.agentEnv.FACTORY_HOME_OVERRIDE = factoryHome
  worker.owner = nativeOwner()
  worker.reads = []
  worker.failure = undefined
  worker.agentIds = []
  worker.onRead = undefined
})

afterEach(() => {
  endWaitLimit?.()
  endWaitLimit = undefined
  rmSync(factoryHome, { recursive: true, force: true })
})

describe('droidSettingsMatch', () => {
  it('requires every expected field in the current native snapshot', () => {
    const state = snapshot({ interactionMode: 'spec', autonomyLevel: 'off' })
    expect(droidSettingsMatch(state, { interactionMode: 'spec', autonomyLevel: 'off' })).toBe(true)
    expect(droidSettingsMatch(state, { interactionMode: 'spec', autonomyLevel: 'high' })).toBe(false)
    expect(droidSettingsMatch(state, { modelId: 'missing' })).toBe(false)
  })

  it('accepts native changes without a LeapMux request receipt', () => {
    expect(droidSettingsMatch(snapshot({ interactionMode: 'auto' }), { interactionMode: 'auto' })).toBe(true)
  })

  it('distinguishes an empty value from an absent field', () => {
    expect(droidSettingsMatch(snapshot({ modelId: '' }), { modelId: '' })).toBe(true)
    expect(droidSettingsMatch(snapshot({}), { modelId: '' })).toBe(false)
  })

  it('refuses an expectation without a setting', () => {
    expect(() => droidSettingsMatch(snapshot({}), {})).toThrow('at least one expected setting')
    expect(() => droidSettingsMatch(snapshot({}), { modelId: undefined })).toThrow('at least one expected setting')
  })
})

describe('readDroidNativeSettings', () => {
  it('reads the exact native session file without changing it or consulting stored frames', async () => {
    const path = writeSettings({ model: 'native-model', interactionMode: 'spec', autonomyLevel: 'off' })
    writeSettings({ model: 'wrong-session' }, 'other')
    writeSettings({ model: 'wrong-directory' }, 'session-1', '-other-work')
    const original = readFileSync(path, 'utf8')
    expect(await readDroidNativeSettings(context, 'droid-agent')).toEqual(snapshot({ modelId: 'native-model', interactionMode: 'spec', autonomyLevel: 'off' }))
    expect(readFileSync(path, 'utf8')).toBe(original)
    expect(worker.agentIds).toEqual(['droid-agent', 'droid-agent'])
  })

  it('reads the latest native file after a setting changes', async () => {
    writeSettings({ interactionMode: 'spec', autonomyLevel: 'off' })
    expect(await readDroidNativeSettings(context, 'droid-agent')).toMatchObject({ interactionMode: 'spec' })
    writeSettings({ interactionMode: 'auto', autonomyLevel: 'high' })
    expect(await readDroidNativeSettings(context, 'droid-agent')).toMatchObject({ interactionMode: 'auto', autonomyLevel: 'high' })
  })

  it('uses the native Windows directory spelling', async () => {
    worker.owner = nativeOwner({ workingDir: 'C:\\native\\work' })
    writeSettings({ model: 'windows-model' }, 'session-1', 'C-native-work')
    expect(await readDroidNativeSettings(context, 'droid-agent')).toMatchObject({ modelId: 'windows-model' })
  })

  it.each([
    { agentSessionId: 'other' },
    { workingDir: '/other/work' },
    { parentAgentId: 'another-parent' },
    { status: AgentStatus.INACTIVE },
  ])('refuses ownership that changes during the file read: %j', async (fields) => {
    writeSettings({ model: 'native-model' })
    worker.reads = [
      nativeOwner(),
      nativeOwner(fields),
    ]
    await expect(readDroidNativeSettings(context, 'droid-agent')).rejects.toThrow('ownership changed')
  })

  it.each([
    { id: 'another-agent' },
    { workerId: 'another-worker' },
    { agentProvider: AgentProvider.CLAUDE_CODE },
    { status: AgentStatus.INACTIVE },
    { agentSessionId: '' },
    { agentSessionId: '../other' },
    { workingDir: 'relative' },
  ])('refuses an invalid initial owner: %j', async (fields) => {
    worker.owner = nativeOwner(fields)
    await expect(readDroidNativeSettings(context, 'droid-agent')).rejects.toThrow('active agent with an exact native session')
  })

  it.each([undefined, '', 'relative'])('refuses an absent or relative isolated home: %s', async (home) => {
    const agentEnv: Record<string, string> = {}
    if (home !== undefined)
      agentEnv.FACTORY_HOME_OVERRIDE = home
    await expect(readDroidNativeSettings({
      leapmuxServer: { ...context.leapmuxServer, agentEnv },
    }, 'droid-agent')).rejects.toThrow('isolated absolute Factory home')
    expect(worker.agentIds).toEqual([])
  })

  it('preserves missing and malformed native file errors', async () => {
    await expect(readDroidNativeSettings(context, 'droid-agent')).rejects.toMatchObject({ code: 'ENOENT' })
    const path = writeSettings({})
    writeFileSync(path, '{bad')
    await expect(readDroidNativeSettings(context, 'droid-agent')).rejects.toThrow(SyntaxError)
  })
})

describe('expectDroidNativeSettings', () => {
  it('waits through a failed read and startup, then reads the selected session file', async () => {
    worker.reads = [
      new Error('The Worker read failed.'),
      nativeOwner({ status: AgentStatus.STARTING }),
    ]
    worker.onRead = () => {
      if (worker.agentIds.length === 3)
        writeSettings({ model: 'native-model' })
    }
    await expectDroidNativeSettings(context, { modelId: 'native-model' })
    expect(worker.agentIds).toEqual([
      'droid-agent',
      'droid-agent',
      'droid-agent',
      'droid-agent',
    ])
  })

  it('reports the native settings when they differ from the expected values', async () => {
    endWaitLimit = startWaitLimitForTests(500)
    writeSettings({ model: 'model-a' })
    await expect(expectDroidNativeSettings(context, { modelId: 'model-b' })).rejects.toThrow(/model-b.*model-a/)
  })

  it('reports the read failure when every read fails', async () => {
    endWaitLimit = startWaitLimitForTests(500)
    worker.failure = new Error('The Worker read failed.')
    await expect(expectDroidNativeSettings(context, { modelId: 'model-b' })).rejects.toThrow('The Worker read failed.')
  })

  it('refuses an empty expectation before it reads any owner', async () => {
    await expect(expectDroidNativeSettings(context, {})).rejects.toThrow('at least one expected setting')
    expect(worker.agentIds).toEqual([])
  })
})
