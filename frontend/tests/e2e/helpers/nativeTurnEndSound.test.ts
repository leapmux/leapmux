import type { MockModelToolCall } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeTurnEndSoundCase } from './nativeTurnEndSound'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { NATIVE_SOUND_COMMAND, nativeSoundActivity, nativeSoundReadTool, planNativeSoundCase } from './nativeTurnEndSound'
import { readToolCall } from './providerToolCalls'

/** The active agent that the scenario reads. A test sets its working directory. */
const activeAgent = vi.hoisted(() => ({ id: 'native-agent', workingDir: '' }))
vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: async () => activeAgent,
}))

const bash: MockModelToolCall = { id: 'bash', name: 'bash', arguments: { command: 'echo SOUND42' } }
const answerTool: MockModelToolCall = { id: 'answer', name: 'answer', arguments: {} }
const textAnswer = { text: 'A text-only answer.' }

describe('nativeSoundActivity', () => {
  it('finds no activity in a text-only script', () => {
    expect(nativeSoundActivity([textAnswer])).toBe(false)
  })

  it('finds no activity in a call that only delivers the provider answer', () => {
    expect(nativeSoundActivity([{ toolCalls: [answerTool] }], ['answer'])).toBe(false)
  })

  it('counts an answer tool as activity when the provider does not state it as an answer tool', () => {
    expect(nativeSoundActivity([{ toolCalls: [answerTool] }])).toBe(true)
  })

  it('finds activity in a real tool, alone or before an answer tool', () => {
    expect(nativeSoundActivity([{ toolCalls: [bash] }])).toBe(true)
    expect(nativeSoundActivity([{ toolCalls: [bash] }, { toolCalls: [answerTool] }], ['answer'])).toBe(true)
  })

  it('finds activity in a step that holds a tool and the answer text', () => {
    expect(nativeSoundActivity([{ toolCalls: [bash], text: 'The tool and the answer in one step.' }])).toBe(true)
  })

  it('finds no activity in an empty script', () => {
    expect(nativeSoundActivity([])).toBe(false)
  })
})

describe('planNativeSoundCase', () => {
  it('answers alone when the case gives no tool', () => {
    expect(planNativeSoundCase({}, textAnswer)).toEqual({ steps: [textAnswer], toolActivity: false })
  })

  it('runs the tool in its own step before the answer', () => {
    expect(planNativeSoundCase({ tool: bash }, textAnswer)).toEqual({ steps: [{ toolCalls: [bash] }, textAnswer], toolActivity: true })
  })

  it('keeps the answer tool of a provider out of the activity', () => {
    const answer = { toolCalls: [answerTool] }
    expect(planNativeSoundCase({}, answer, ['answer'])).toEqual({ steps: [answer], toolActivity: false })
    expect(planNativeSoundCase({ tool: bash }, answer, ['answer'])).toEqual({ steps: [{ toolCalls: [bash] }, answer], toolActivity: true })
  })

  it('uses a custom script unchanged and derives its activity', () => {
    const steps = [{ toolCalls: [bash], text: 'The tool and the answer in one step.' }]
    expect(planNativeSoundCase({ steps }, textAnswer)).toEqual({ steps, toolActivity: true })
    expect(planNativeSoundCase({ steps: [textAnswer] }, { text: 'unused' })).toEqual({ steps: [textAnswer], toolActivity: false })
  })

  it('refuses a tool together with a custom script', () => {
    expect(() => planNativeSoundCase({ tool: bash, steps: [textAnswer] }, textAnswer)).toThrow('its tool or its custom script, not both')
  })

  it('refuses an empty custom script', () => {
    expect(() => planNativeSoundCase({ steps: [] }, textAnswer)).toThrow('needs a model step')
  })

  it('refuses an approval when the script runs no tool apart from the answer tools', () => {
    expect(() => planNativeSoundCase({ approveTool: true }, textAnswer)).toThrow('approves a tool only when its script runs a tool')
    expect(() => planNativeSoundCase({ approveTool: true }, { toolCalls: [answerTool] }, ['answer'])).toThrow('approves a tool only when its script runs a tool')
    expect(planNativeSoundCase({ tool: bash, approveTool: true }, textAnswer).toolActivity).toBe(true)
  })
})

describe('NativeTurnEndSoundCase', () => {
  it('refuses a tool together with a custom script at compile time', () => {
    // @ts-expect-error A sound case gives its tool or its custom script, not both.
    const both: NativeTurnEndSoundCase = { tool: bash, steps: [textAnswer] }
    expect(() => planNativeSoundCase(both, textAnswer)).toThrow('not both')
  })
})

describe('nativeSoundReadTool', () => {
  let directory: string

  beforeEach(() => {
    const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
    mkdirSync(scratch, { recursive: true })
    directory = mkdtempSync(join(scratch, 'native-sound-read-'))
    activeAgent.workingDir = directory
  })

  afterEach(() => rmSync(directory, { recursive: true, force: true }))

  const context = { provider: AgentProvider.AMP } as ManagedNativeScenarioContext

  it('writes the file in the working directory of the active agent and returns the provider read call of that file', async () => {
    const call = await nativeSoundReadTool(context)
    const path = join(directory, 'native-notification-file.txt')
    expect(readFileSync(path, 'utf8')).toBe('Native notification file contents.\n')
    expect(call).toEqual(readToolCall(AgentProvider.AMP, 'notification-read', path))
  })

  it('gives the read call the stated call ID', async () => {
    const call = await nativeSoundReadTool(context, 'second-sound-read')
    expect(call).toEqual(readToolCall(AgentProvider.AMP, 'second-sound-read', join(directory, 'native-notification-file.txt')))
  })
})

describe('NATIVE_SOUND_COMMAND', () => {
  it('holds no shell expansion and no newline, which make some providers ask for a permission', () => {
    expect(NATIVE_SOUND_COMMAND).not.toMatch(/[$`\n\r]/)
    expect(NATIVE_SOUND_COMMAND.trim()).toBe(NATIVE_SOUND_COMMAND)
  })
})
