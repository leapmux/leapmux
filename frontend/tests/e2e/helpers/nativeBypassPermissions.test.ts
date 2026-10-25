/**
 * These unit tests check the order of a permission shortcut test with fake dependencies.
 * The browser specs check the actual agents for both permission shortcuts.
 */
import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeLocator } from '~/test-support/fakeLocator'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBypassPermissions, exerciseSmartPermissions } from './nativeBypassPermissions'

const fake = vi.hoisted(() => ({ events: [] as string[], runDir: '', workingDir: '', waits: [] as number[], reads: [] as number[], commands: [] as string[] }))

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
  return fakeLocator((check) => {
    fake.events.push(`${name} ${check.expression}`)
    return true
  })
}

const scratchRoot = resolve(process.cwd(), '../.tmp')

beforeEach(() => {
  fake.events = []
  fake.waits = []
  fake.reads = []
  fake.commands = []
  mkdirSync(scratchRoot, { recursive: true })
  fake.runDir = mkdtempSync(join(scratchRoot, 'native-shortcut-unit-'))
  fake.workingDir = join(fake.runDir, 'workspace')
  mkdirSync(fake.workingDir)
})
afterEach(() => rmSync(fake.runDir, { recursive: true, force: true }))

/**
 * The model script simulates the agent. Each answered step removes the target, as the native command does.
 * The result request holds the command output.
 */
function shortcutContext(preset: 'smart' | 'bypass', options: { removeTarget?: boolean } = {}): ManagedNativeScenarioContext {
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
    queue: async (...steps: { toolCalls?: Array<{ id: string, arguments: { command: string } }> }[]) => {
      const step = steps[0]
      if (!step)
        throw new Error('The native shortcut needs a tool step.')
      toolIds.push(step.toolCalls?.[0]?.id ?? '')
      fake.commands.push(step.toolCalls?.[0]?.arguments.command ?? '')
      fake.events.push(`queue:${toolIds.at(-1)}`)
      const start = queued
      queued += steps.length
      return start
    },
    waitForSteps: async (count: number) => {
      fake.waits.push(count)
      if (options.removeTarget !== false)
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
  return { page, modelScript, provider: AgentProvider.CLAUDE_CODE, providerAgent: { provider: AgentProvider.CLAUDE_CODE, prefix: 'native-e2e' }, workspaceId: 'workspace', leapmuxServer: { hubUrl: '', adminToken: '', workerId: '' } }
}

describe('exerciseBypassPermissions', () => {
  it('uses one request when the native exchange contains both tools and the answer', async () => {
    const proof = { answerStep: 'same-step' as const, settingsProof: () => {} }
    await exerciseBypassPermissions(shortcutContext('bypass'), proof)
    expect(fake.waits).toEqual([1, 2])
    expect(fake.reads).toEqual([0, 1])
    expect(fake.commands).toHaveLength(2)
    expect(existsSync(join(fake.workingDir, 'native-bypass-proof'))).toBe(false)
  })

  it('prepares the session before both passes and reloads between them', async () => {
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
    // Each pass starts at its own step index:
    // - Queue two steps.
    // - Wait for both steps.
    // - Read the request for the answer step.
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

  it('uses each custom command and awaits its proof before the next pass', async () => {
    const results: string[] = []
    await exerciseSmartPermissions(shortcutContext('smart'), {
      commandProof: (target, pass) => {
        expect(existsSync(join(target, 'keep.txt'))).toBe(true)
        expect(results).toHaveLength(pass - 1)
        return {
          command: `custom-removal-${pass}`,
          resultProof: async (outcome) => {
            await Promise.resolve()
            results.push(outcome.text)
          },
        }
      },
    })

    expect(fake.commands).toEqual(['custom-removal-1', 'custom-removal-2'])
    expect(results).toEqual(['SMART-1-42\n', 'SMART-2-42\n'])
    expect(fake.reads).toEqual([1, 3])
    expect(fake.events.filter(event => event.startsWith('answer '))).toHaveLength(2)
    expect(fake.events).toContain('reload')
  })

  it('stops when the custom result proof rejects', async () => {
    await expect(exerciseSmartPermissions(shortcutContext('smart'), {
      commandProof: () => ({
        command: 'custom-removal',
        resultProof: async () => {
          await Promise.resolve()
          throw new Error('The native result failed verification.')
        },
      }),
    })).rejects.toThrow('The native result failed verification.')

    expect(fake.commands).toEqual(['custom-removal'])
    expect(fake.events).not.toContain('reload')
  })

  it('requires target removal even when the custom result proof accepts the result', async () => {
    const context = shortcutContext('smart', { removeTarget: false })
    const resultProof = vi.fn()

    await expect(exerciseSmartPermissions(context, {
      commandProof: () => ({ command: 'custom-removal', resultProof }),
    })).rejects.toThrow('The native command must remove its target.')

    expect(existsSync(join(fake.workingDir, 'native-smart-proof', 'keep.txt'))).toBe(true)
    expect(resultProof).not.toHaveBeenCalled()
    expect(fake.events).not.toContain('reload')
  })

  it('stops before a model request when the command factory throws', async () => {
    await expect(exerciseSmartPermissions(shortcutContext('smart'), {
      commandProof: () => { throw new Error('The command is unavailable.') },
    })).rejects.toThrow('The command is unavailable.')

    expect(fake.commands).toEqual([])
    expect(fake.events).not.toContain('send')
    expect(fake.events).not.toContain('reload')
  })
})
