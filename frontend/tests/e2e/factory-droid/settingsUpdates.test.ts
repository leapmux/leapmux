import type { Page } from '@playwright/test'
import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { DroidNativeSettingsUpdate } from '../helpers/droidSettingsFrame'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { startWaitLimitForTests } from '../helpers/testDeadline'
import { droidSettingsMatch, expectDroidNativeSettings, readDroidNativeSettings } from './settingsUpdates'

/** The stored messages that each Worker read returns, one list for each read. */
const worker = vi.hoisted(() => ({ reads: [] as (unknown[] | Error)[], agentIds: [] as string[] }))

vi.mock('../helpers/nativeMessages', () => ({
  readAllAgentMessages: async (_context: unknown, agentId: string) => {
    worker.agentIds.push(agentId)
    const read = worker.reads.length > 1 ? worker.reads.shift() : worker.reads[0]
    if (!read || read instanceof Error)
      throw read ?? new Error('The test supplied no Worker read.')
    return read
  },
}))

vi.mock('../helpers/nativeScenario', () => ({ selectedAgentTabId: async () => 'droid-agent' }))

/** The end of the test record that limits a wait which never passes. */
let endWaitLimit: (() => void) | undefined

afterEach(() => {
  endWaitLimit?.()
  endWaitLimit = undefined
})

/** Limit the wait of the check to a short time, so that a check that never passes fails inside the unit test. */
function limitWait(): void {
  endWaitLimit = startWaitLimitForTests(500)
}

function update(fields: Partial<DroidNativeSettingsUpdate>): DroidNativeSettingsUpdate {
  return { requestId: undefined, modelId: undefined, reasoningEffort: undefined, interactionMode: undefined, autonomyLevel: undefined, ...fields }
}

function storedMessage(body: unknown, compression = ContentCompression.NONE): AgentChatMessage {
  const text = typeof body === 'string' ? body : JSON.stringify(body)
  return { content: new TextEncoder().encode(text), contentCompression: compression } as unknown as AgentChatMessage
}

const context = { page: {} as Page, leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' } }

beforeEach(() => {
  worker.reads = []
  worker.agentIds = []
})

describe('droidSettingsMatch', () => {
  const leapmuxSpec = update({ requestId: 'leapmux-3', interactionMode: 'spec', autonomyLevel: 'off' })

  it('accepts an event that answers a LeapMux request and states each expected setting', () => {
    expect(droidSettingsMatch([leapmuxSpec], { interactionMode: 'spec', autonomyLevel: 'off' })).toBe(true)
  })

  it('refuses the expected settings in an event that Droid sent by itself', () => {
    expect(droidSettingsMatch([{ ...leapmuxSpec, requestId: 'native-1' }], { interactionMode: 'spec' })).toBe(false)
    expect(droidSettingsMatch([{ ...leapmuxSpec, requestId: undefined }], { interactionMode: 'spec' })).toBe(false)
  })

  it('refuses an event that states only part of the expected settings', () => {
    expect(droidSettingsMatch([leapmuxSpec], { interactionMode: 'spec', autonomyLevel: 'high' })).toBe(false)
  })

  it('reads the last event that states each expected field for the latest state, whatever sent it', () => {
    const later = update({ requestId: 'native-2', interactionMode: 'auto', autonomyLevel: 'off' })
    const modelOnly = update({ requestId: 'native-3', modelId: 'custom:Droid-0' })
    expect(droidSettingsMatch([leapmuxSpec, later, modelOnly], { interactionMode: 'auto', autonomyLevel: 'off' }, 'latest')).toBe(true)
    expect(droidSettingsMatch([leapmuxSpec, later, modelOnly], { interactionMode: 'spec' }, 'latest')).toBe(false)
    expect(droidSettingsMatch([], { interactionMode: 'spec' }, 'latest')).toBe(false)
  })

  it('refuses an expectation without a setting', () => {
    expect(() => droidSettingsMatch([leapmuxSpec], {})).toThrow('at least one expected setting')
    expect(() => droidSettingsMatch([leapmuxSpec], { modelId: undefined })).toThrow('at least one expected setting')
  })
})

describe('readDroidNativeSettings', () => {
  it('reads the settings events of every stored message in order, and skips the other rows', async () => {
    worker.reads = [[
      storedMessage({ type: 'settings_updated', requestId: 'leapmux-1', settings: { modelId: 'model-a' } }),
      storedMessage('{not json'),
      storedMessage({ type: 'tool_result' }),
      storedMessage({ type: 'notification_thread', messages: [{ type: 'settings_updated', settings: { reasoningEffort: 'high' } }] }),
      storedMessage({ type: 'settings_updated', settings: { modelId: 'model-b' } }, 99 as ContentCompression),
    ]]
    expect(await readDroidNativeSettings(context, 'droid-agent')).toEqual([
      update({ requestId: 'leapmux-1', modelId: 'model-a' }),
      update({ reasoningEffort: 'high' }),
    ])
  })
})

describe('expectDroidNativeSettings', () => {
  it('waits through a read that fails and a read without the event, and reads the agent on screen', async () => {
    worker.reads = [
      new Error('The Worker read failed while Droid restarted.'),
      [],
      [storedMessage({ type: 'settings_updated', requestId: 'leapmux-1', settings: { modelId: 'model-a' } })],
    ]
    await expectDroidNativeSettings(context, { modelId: 'model-a' })
    expect(new Set(worker.agentIds)).toEqual(new Set(['droid-agent']))
  })

  it('fails with the settings events that Droid reported', async () => {
    limitWait()
    worker.reads = [[storedMessage({ type: 'settings_updated', requestId: 'leapmux-1', settings: { modelId: 'model-a' } })]]
    await expect(expectDroidNativeSettings(context, { modelId: 'model-b' })).rejects.toThrow(/model-b.*model-a/)
  })

  it('states the failed read when every read fails', async () => {
    limitWait()
    worker.reads = [new Error('The Worker read failed.')]
    await expect(expectDroidNativeSettings(context, { modelId: 'model-b' })).rejects.toThrow('The Worker read failed.')
  })

  it('refuses an expectation without a setting before it reads anything', async () => {
    await expect(expectDroidNativeSettings(context, {})).rejects.toThrow('at least one expected setting')
    expect(worker.agentIds).toEqual([])
  })
})
