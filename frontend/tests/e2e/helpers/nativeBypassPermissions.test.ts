/**
 * The unit tests check the order of a permission shortcut proof against fakes of the page, the model script, and the
 * Worker. The bypass-permissions-shortcut and smart-permissions-shortcut browser specs check the actual agents.
 */
import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBypassPermissions, exerciseSmartPermissions } from './nativeBypassPermissions'

const fake = vi.hoisted(() => ({ events: [] as string[], runDir: '', workingDir: '', waits: [] as number[], reads: [] as number[] }))

vi.mock('./server', () => ({ getGlobalState: () => ({ tmpDir: fake.runDir }) }))
vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: async () => {
    fake.events.push('agent')
    return { id: 'agent-1', workingDir: fake.workingDir, optionGroups: [] }
  },
}))
vi.mock('./nativeControlObservation', () => ({
  expectNoNativeControl: async (_context: unknown, options: { testId: string, relatedProof: () => Promise<void> }) => {
    fake.events.push(`observe:${options.testId}`)
    await options.relatedProof()
  },
}))
vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  applyPermissionPreset: async (_page: unknown, kind: string) => { fake.events.push(`preset:${kind}`) },
  waitForSettingsIdle: async () => { fake.events.push('settings-idle') },
  waitForNativeSettingsHydrated: async () => { fake.events.push('hydrated') },
  sendMessage: async () => { fake.events.push('send') },
  waitForAgentIdle: async () => { fake.events.push('idle') },
  messageBubbles: () => ({ filter: ({ hasText }: { hasText: string }) => ({ first: () => passingLocator(`output ${hasText}`) }) }),
  assistantBubbles: () => ({ filter: ({ hasText }: { hasText: string }) => ({ first: () => passingLocator(`answer ${hasText}`) }) }),
}))
vi.mock('./providerToolCalls', () => ({ bashToolCall: (_provider: AgentProvider, id: string, command: string) => ({ id, name: 'unit-native-shell', arguments: { command } }) }))

/** A fake locator whose Playwright checks pass and leave a record. */
function passingLocator(name: string): Locator {
  class FakeLocator {
    readonly _apiName = 'Locator'
    async _expect(expression: string) {
      fake.events.push(`${name} ${expression}`)
      return { matches: true, received: true, log: [], timedOut: false }
    }
  }
  return new FakeLocator() as unknown as Locator
}

const scratchRoot = resolve(process.cwd(), '../.tmp')

beforeEach(() => {
  fake.events = []
  fake.waits = []
  fake.reads = []
  mkdirSync(scratchRoot, { recursive: true })
  fake.runDir = mkdtempSync(join(scratchRoot, 'native-shortcut-unit-'))
  fake.workingDir = join(fake.runDir, 'workspace')
  mkdirSync(fake.workingDir)
})
afterEach(() => rmSync(fake.runDir, { recursive: true, force: true }))

/**
 * A context whose model script plays the agent: each answered step removes the target, as the native command does,
 * and the result request holds the output of the command.
 */
function shortcutContext(preset: 'smart' | 'bypass'): ManagedNativeScenarioContext {
  const target = () => join(fake.workingDir, `native-${preset}-proof`)
  let queued = 0
  const toolIds: string[] = []
  const page = Object.assign({} as Page, {
    reload: async () => {
      fake.events.push('reload')
      return null
    },
  })
  const modelScript = {
    prompt: (text: string) => text,
    queue: async (step: { toolCalls?: Array<{ id: string }> }) => {
      toolIds.push(step.toolCalls?.[0]?.id ?? '')
      fake.events.push(`queue:${toolIds.at(-1)}`)
      const start = queued
      queued += 2
      return start
    },
    waitForSteps: async (count: number) => {
      fake.waits.push(count)
      rmSync(target(), { recursive: true, force: true })
    },
    requestAt: async (index: number): Promise<MockModelRequestRecord> => {
      fake.reads.push(index)
      return {
        protocol: 'openai-chat-completions',
        path: '/chat/completions',
        // The ID of the tool call ends with the index of its pass, and the output holds the pass from 1.
        body: { messages: [{ role: 'tool', tool_call_id: toolIds.at(-1), content: `${preset.toUpperCase()}-${Number(toolIds.at(-1)?.at(-1)) + 1}-42\n` }] },
      }
    },
  } as unknown as ModelScript
  return { page, modelScript, provider: AgentProvider.CLAUDE_CODE, workspaceId: 'workspace', leapmuxServer: { hubUrl: '', adminToken: '', workerId: '' } }
}

describe('exerciseBypassPermissions', () => {
  it('prepares the session before it reads the agent and applies the preset, and runs one pass before and one after a reload', async () => {
    await exerciseBypassPermissions(shortcutContext('bypass'), {
      prepare: async () => { fake.events.push('prepare') },
      settingsProof: () => { fake.events.push('settings') },
    })
    expect(fake.events).toEqual([
      'prepare',
      'agent',
      'preset:bypass',
      'settings-idle',
      'agent',
      'settings',
      'observe:control-banner',
      'queue:native-bypass-0',
      'send',
      'idle',
      'output BYPASS-1-42 to.be.visible',
      'answer The native bypass command completed in pass 1. to.be.visible',
      'reload',
      'hydrated',
      'agent',
      'settings',
      'observe:control-banner',
      'queue:native-bypass-1',
      'send',
      'idle',
      'output BYPASS-2-42 to.be.visible',
      'answer The native bypass command completed in pass 2. to.be.visible',
    ])
    expect(existsSync(join(fake.workingDir, 'native-bypass-proof'))).toBe(false)
    // Each pass queues two steps from its own start, waits for both, and reads the request of its answer step.
    expect(fake.waits).toEqual([2, 4])
    expect(fake.reads).toEqual([1, 3])
  })

  it('applies no preset when the preparation fails', async () => {
    await expect(exerciseBypassPermissions(shortcutContext('bypass'), { prepare: async () => {
      throw new Error('the default mode is not offered')
    } })).rejects.toThrow('the default mode is not offered')
    expect(fake.events).toEqual([])
  })
})

describe('exerciseSmartPermissions', () => {
  it('runs the Smart preset with its own target and output', async () => {
    await exerciseSmartPermissions(shortcutContext('smart'))
    expect(fake.events).toContain('preset:smart')
    expect(fake.events.filter(event => event.startsWith('queue:'))).toEqual(['queue:native-smart-0', 'queue:native-smart-1'])
  })
})
