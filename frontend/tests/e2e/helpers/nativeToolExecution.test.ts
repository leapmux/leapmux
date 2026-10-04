import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelScenarioStatus } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { createNativeToolDirectory } from './nativeToolDirectory'
import { clickNativeToolApproval, nativeFileEditSequence, nativeFileReadResult, nativeFileWriteSequence, nativeToolResultAt, processNativeToolApproval, waitForNativeToolSteps } from './nativeToolExecution'
import { quotePosixShellArgument } from './shellArguments'

const calls = vi.hoisted(() => ({ shell: vi.fn(), read: vi.fn(), edit: vi.fn(), write: vi.fn(), idle: vi.fn() }))
vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  waitForAgentIdle: calls.idle,
}))
vi.mock('./providerToolCalls', () => ({
  bashToolCall: calls.shell,
  readToolCall: calls.read,
  editToolCall: calls.edit,
  writeToolCall: calls.write,
}))

const scratchRoot = resolve(process.cwd(), '../.tmp')
let directory: string
beforeEach(() => {
  vi.resetAllMocks()
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'native-file-sequence-unit-'))
  for (const [call, name] of [[calls.shell, 'unit-shell'], [calls.read, 'unit-read'], [calls.edit, 'unit-edit'], [calls.write, 'unit-write']] as const)
    call.mockImplementation((_provider, id) => ({ id, name }))
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

/** Keep a narrow browser fixture honest when the helper accesses a new method. */
function guardedBrowserHandle<T extends object>(methods: Partial<T>): T {
  return new Proxy(methods as T, {
    get: (target, property, receiver) => {
      if (property in target)
        return Reflect.get(target, property, receiver)
      if (typeof property === 'symbol')
        return undefined
      throw new Error(`The native tool wait fixture lacks ${String(property)}.`)
    },
  })
}

describe('waitForNativeToolSteps', () => {
  function fixture() {
    const events: string[] = []
    const status: MockModelScenarioStatus = { complete: true, nextStep: 2, stepCount: 2, requests: [], unexpectedRequests: [], ruleMatches: {}, pendingGates: [] }
    const first = guardedBrowserHandle<Locator>({})
    const locator = guardedBrowserHandle<Locator>({ first: () => first })
    const page = guardedBrowserHandle<Page>({ locator: () => locator })
    const context: NativeScenarioContext = {
      page,
      provider: AgentProvider.QODER,
      modelScript: {
        id: 'completed-tool-wait',
        testDeadline: () => undefined,
        prompt: text => text,
        queue: async () => {},
        rule: async () => {},
        fallback: async () => {},
        status: async () => status,
        waitForSteps: async () => {
          events.push('model-completed')
          return status
        },
        waitForGate: async () => status,
        releaseGate: async () => {},
        releaseGateIfHeld: async () => false,
        allowUnconsumed: () => {},
      },
    }
    calls.idle.mockImplementation(async () => {
      events.push('idle')
    })
    // This signature lets the behavioral regression compile before the handler exists.
    const observeWait: (context: NativeScenarioContext, target: number, options: { beforeIdle: () => Promise<void> }) => Promise<void> = waitForNativeToolSteps
    return { context, events, observeWait }
  }

  it('captures native evidence after the exact model receipt and before the idle wait', async () => {
    const current = fixture()
    await current.observeWait(current.context, 2, { beforeIdle: async () => {
      current.events.push('capture')
    } })
    expect(current.events).toEqual(['model-completed', 'capture', 'idle'])
  })

  it('preserves a capture failure and does not start the idle wait', async () => {
    const current = fixture()
    const cause = new Error('The native Worker evidence read failed.')
    await expect(current.observeWait(current.context, 2, { beforeIdle: async () => {
      throw cause
    } })).rejects.toBe(cause)
    expect(calls.idle).not.toHaveBeenCalled()
  })

  it('preserves an idle failure after the native evidence handler completes', async () => {
    const current = fixture()
    const cause = new Error('The native turn remains active.')
    calls.idle.mockImplementation(async () => {
      current.events.push('idle')
      throw cause
    })
    await expect(current.observeWait(current.context, 2, { beforeIdle: async () => {
      current.events.push('capture')
    } })).rejects.toBe(cause)
    expect(current.events).toEqual(['model-completed', 'capture', 'idle'])
  })
})

describe('processNativeToolApproval', () => {
  it('uses the exact completed model receipt when the actual approval disappears before click', async () => {
    const button = document.createElement('button')
    document.body.append(button)
    const target = 2
    let nextStep = 1
    const clickIfReady = vi.fn(async () => {
      button.remove()
      nextStep = target
      return clickNativeToolApproval([])
    })
    try {
      const forbiddenVisibilityRead = vi.fn(async () => true)
      const forbiddenEnabledRead = vi.fn(async () => {
        throw new Error('The removed control cannot receive a separate enabled read.')
      })
      const control = { completed: async () => nextStep >= target, clickIfReady, isVisible: forbiddenVisibilityRead, isEnabled: forbiddenEnabledRead }
      await expect(processNativeToolApproval(control)).resolves.toBe('completed')
      expect(forbiddenVisibilityRead).not.toHaveBeenCalled()
      expect(forbiddenEnabledRead).not.toHaveBeenCalled()
      expect(nextStep).toBe(target)
      expect(button.isConnected).toBe(false)
      expect(clickIfReady).toHaveBeenCalledTimes(1)
    }
    finally {
      button.remove()
    }
  })
  it('keeps the exact model target pending when no native approval exists', async () => {
    const clickIfReady = vi.fn(async () => false)
    expect(await processNativeToolApproval({ completed: async () => false, clickIfReady })).toBe('waiting')
    expect(clickIfReady).toHaveBeenCalledTimes(1)
  })
  it('does not click after the exact native model target completes', async () => {
    const clickIfReady = vi.fn(async () => true)
    expect(await processNativeToolApproval({ completed: async () => true, clickIfReady })).toBe('completed')
    expect(clickIfReady).not.toHaveBeenCalled()
  })
  it('counts an actual approval while the exact model target remains pending', async () => {
    const clickIfReady = vi.fn(async () => true)
    expect(await processNativeToolApproval({ completed: async () => false, clickIfReady })).toBe('approval')
    expect(clickIfReady).toHaveBeenCalledTimes(1)
  })
  it('propagates an unrelated native approval operation failure', async () => {
    const cause = new Error('The browser execution context failed.')
    await expect(processNativeToolApproval({
      completed: async () => false,
      clickIfReady: async () => {
        throw cause
      },
    })).rejects.toBe(cause)
  })
})

describe('clickNativeToolApproval', () => {
  function visibleButton(): HTMLButtonElement {
    const button = document.createElement('button')
    document.body.append(button)
    const rectangle = new DOMRect(0, 0, 100, 20)
    const rectangles = Object.assign([rectangle], { item: (index: number) => index === 0 ? rectangle : null })
    vi.spyOn(button, 'getClientRects').mockReturnValue(rectangles)
    return button
  }
  it('clicks the same enabled button synchronously and counts only an actual click', () => {
    const button = visibleButton()
    let clicks = 0
    button.onclick = () => {
      clicks++
      button.disabled = true
    }
    try {
      expect(clickNativeToolApproval([button])).toBe(true)
      expect(clicks).toBe(1)
      expect(button.disabled).toBe(true)
      expect(clickNativeToolApproval([button])).toBe(false)
      expect(clicks).toBe(1)
    }
    finally {
      button.remove()
    }
  })
  it('does not repeat an approval while its actual response fieldset is disabled', () => {
    const fieldset = document.createElement('fieldset')
    const button = visibleButton()
    fieldset.append(button)
    document.body.append(fieldset)
    const click = vi.spyOn(button, 'click')
    button.onclick = () => {
      fieldset.disabled = true
    }
    try {
      expect(clickNativeToolApproval([button])).toBe(true)
      expect(button.disabled).toBe(false)
      expect(clickNativeToolApproval([button])).toBe(false)
      expect(click).toHaveBeenCalledTimes(1)
    }
    finally {
      fieldset.remove()
    }
  })
  it.each(['disabled', 'hidden', 'detached', 'display-none', 'visibility-hidden'])('does not click a %s native control', (state) => {
    const button = visibleButton()
    const click = vi.spyOn(button, 'click')
    if (state === 'disabled')
      button.disabled = true
    if (state === 'hidden')
      button.hidden = true
    if (state === 'detached')
      button.remove()
    if (state === 'display-none')
      button.style.display = 'none'
    if (state === 'visibility-hidden')
      button.style.visibility = 'hidden'
    try {
      expect(clickNativeToolApproval([button])).toBe(false)
      expect(click).not.toHaveBeenCalled()
    }
    finally {
      button.remove()
    }
  })
  it('refuses an enabled approval after the actual click limit, but permits an absent control', () => {
    const button = visibleButton()
    const click = vi.spyOn(button, 'click')
    try {
      expect(() => clickNativeToolApproval([button], false)).toThrow('approval limit')
      expect(click).not.toHaveBeenCalled()
      expect(clickNativeToolApproval([], false)).toBe(false)
    }
    finally {
      button.remove()
    }
  })
  it('refuses an ambiguous selected control', () => {
    const first = visibleButton()
    const second = visibleButton()
    try {
      expect(() => clickNativeToolApproval([first, second])).toThrow('one selected control')
    }
    finally {
      first.remove()
      second.remove()
    }
  })
  it('refuses an element that is not the actual native approval button', () => {
    const element = document.createElement('div')
    document.body.append(element)
    try {
      expect(() => clickNativeToolApproval([element])).toThrow('actual button')
    }
    finally {
      element.remove()
    }
  })
})

describe('nativeToolResultAt', () => {
  function modelScript(requests: MockModelRequestRecord[]) {
    const status: MockModelScenarioStatus = { complete: true, nextStep: 2, stepCount: 2, ruleMatches: {}, pendingGates: [], requests, unexpectedRequests: [] }
    return { waitForSteps: vi.fn(async () => status) }
  }

  it('waits for the exact queued step and reads only its exact call result', async () => {
    const script = modelScript([
      { protocol: 'openai-chat-completions', path: '/chat/completions', stepIndex: 0, body: { messages: [{ role: 'tool', tool_call_id: 'selected', content: 'WRONG_EARLIER_RESULT' }] } },
      { protocol: 'openai-chat-completions', path: '/chat/completions', stepIndex: 1, body: { messages: [{ role: 'assistant', tool_calls: [{ id: 'selected', function: { arguments: 'ARGUMENT_ONLY_RESULT' } }] }, { role: 'tool', tool_call_id: 'other', content: 'WRONG_CALL_RESULT' }, { role: 'tool', tool_call_id: 'selected', content: 'ACTUAL_SELECTED_RESULT' }] } },
    ])
    expect(await nativeToolResultAt(script, 1, 'selected')).toBe('ACTUAL_SELECTED_RESULT')
    expect(script.waitForSteps).toHaveBeenCalledWith(2)
  })

  it('retains queued step zero', async () => {
    const script = modelScript([{ protocol: 'openai-responses', path: '/responses', stepIndex: 0, body: { input: [{ type: 'function_call_output', call_id: 'zero', output: 'STEP_ZERO_RESULT' }] } }])
    expect(await nativeToolResultAt(script, 0, 'zero')).toBe('STEP_ZERO_RESULT')
    expect(script.waitForSteps).toHaveBeenCalledWith(1)
  })

  it('rejects an absent queued request', async () => {
    await expect(nativeToolResultAt(modelScript([]), 1, 'selected')).rejects.toThrow('no request at step 1')
  })

  it('rejects the wrong call ID even when its text resembles the requested result', async () => {
    const script = modelScript([{ protocol: 'anthropic-messages', path: '/v1/messages', stepIndex: 1, body: { messages: [{ role: 'user', content: [{ type: 'tool_result', tool_use_id: 'other', content: 'EXPECTED_LOOKING_RESULT' }] }] } }])
    await expect(nativeToolResultAt(script, 1, 'selected')).rejects.toThrow(/result|call/)
  })

  it('rejects duplicate results for the exact call', async () => {
    const script = modelScript([{ protocol: 'openai-chat-completions', path: '/chat/completions', stepIndex: 1, body: { messages: [{ role: 'tool', tool_call_id: 'selected', content: 'FIRST' }, { role: 'tool', tool_call_id: 'selected', content: 'SECOND' }] } }])
    await expect(nativeToolResultAt(script, 1, 'selected')).rejects.toThrow(/2 results|received 2/)
  })

  it.each([-1, 0.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('rejects an invalid step index before model access: %s', async (stepIndex) => {
    const script = modelScript([])
    await expect(nativeToolResultAt(script, stepIndex, 'selected')).rejects.toThrow('valid queued-step index')
    expect(script.waitForSteps).not.toHaveBeenCalled()
  })

  it('rejects an empty call ID before model access', async () => {
    const script = modelScript([])
    await expect(nativeToolResultAt(script, 0, '')).rejects.toThrow('tool call ID')
    expect(script.waitForSteps).not.toHaveBeenCalled()
  })

  it('rejects the largest safe index before its wait target becomes unsafe', async () => {
    const script = modelScript([])
    await expect(nativeToolResultAt(script, Number.MAX_SAFE_INTEGER, 'selected')).rejects.toThrow('valid queued-step index')
    expect(script.waitForSteps).not.toHaveBeenCalled()
  })

  it('retains the largest index whose wait target stays safe', async () => {
    const stepIndex = Number.MAX_SAFE_INTEGER - 1
    const script = modelScript([{ protocol: 'openai-responses', path: '/responses', stepIndex, body: { input: [{ type: 'function_call_output', call_id: 'selected', output: 'LAST_SAFE_RESULT' }] } }])
    expect(await nativeToolResultAt(script, stepIndex, 'selected')).toBe('LAST_SAFE_RESULT')
    expect(script.waitForSteps).toHaveBeenCalledWith(Number.MAX_SAFE_INTEGER)
  })
})

describe('nativeFileReadResult', () => {
  it('rejects a stale exact Read even when the model context carries scripted NEW edit arguments', async () => {
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role: 'assistant', tool_calls: [{ id: 'native-edit', function: { arguments: '{"old_string":"OLD42","new_string":"NEW42"}' } }] },
      { role: 'tool', tool_call_id: 'native-read-after', content: 'OLD42\n' },
    ] } }
    await expect(nativeFileReadResult(request, 'native-read-after', 'NEW42', 'OLD42')).rejects.toThrow('expected current bytes')
  })
  it('reads the exact current call while excluding old results from another call', async () => {
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role: 'tool', tool_call_id: 'native-read-before', content: 'OLD42\n' },
      { role: 'tool', tool_call_id: 'native-read-after', content: 'NEW42\n' },
    ] } }
    expect(await nativeFileReadResult(request, 'native-read-after', 'NEW42', 'OLD42')).toBe('NEW42\n')
  })
  it('rejects an actual failed provider result even when its output contains the expected marker', async () => {
    const request: MockModelRequestRecord = { protocol: 'openai-responses', path: '/v1/responses', body: {} }
    await expect(nativeFileReadResult(request, 'native-read-after', 'NEW42', 'OLD42', () => ({ text: 'NEW42', exitCode: 7, failed: true }))).rejects.toThrow('Read returned a failure')
  })
})

describe('nativeFileEditSequence', () => {
  it.runIf(existsSync('/bin/sh'))('seeds the exact bytes without expanding shell characters in the filename', () => {
    const fileName = 'literal $(touch command-expanded-marker) \'quote\';.txt'
    const sequence = nativeFileEditSequence(AgentProvider.CODEBUDDY, { workingDir: directory, fileName })
    const command: unknown = calls.shell.mock.calls[0]?.[2]
    if (typeof command !== 'string')
      throw new Error('The native file seed supplied no shell command.')
    execFileSync('/bin/sh', ['-c', command], { cwd: directory })
    expect(readFileSync(sequence.filePath, 'utf8')).toBe('const parityBefore = 1\n')
    expect(existsSync(join(directory, 'command-expanded-marker'))).toBe(false)
    expect(calls.read).toHaveBeenCalledWith(AgentProvider.CODEBUDDY, 'read-file', sequence.filePath)
    expect(calls.edit).toHaveBeenCalledWith(AgentProvider.CODEBUDDY, 'edit-file', {
      path: sequence.filePath,
      before: 'const parityBefore = 1',
      after: 'const parityAfter = 2',
    })
  })

  it('preserves native shell, read, and edit call order', () => {
    const sequence = nativeFileEditSequence(AgentProvider.QODER, { workingDir: directory, fileName: 'qoder-file-probe.txt' })
    expect(sequence.steps.slice(0, 3).flatMap(step => step.toolCalls?.map(call => call.id) ?? []))
      .toEqual(['seed-file', 'read-file', 'edit-file'])
    expect(calls.shell.mock.invocationCallOrder[0]).toBeLessThan(calls.read.mock.invocationCallOrder[0]!)
    expect(calls.read.mock.invocationCallOrder[0]).toBeLessThan(calls.edit.mock.invocationCallOrder[0]!)
  })
})

describe('nativeFileWriteSequence', () => {
  it('passes the full native Write bytes to the provider vocabulary without a fixture write', () => {
    const sequence = nativeFileWriteSequence(AgentProvider.QODER, { workingDir: directory, fileName: 'qoder-written-probe.txt' })
    expect(calls.write).toHaveBeenCalledWith(AgentProvider.QODER, 'write-file', { path: sequence.filePath, content: 'written-42\n' })
    expect(existsSync(sequence.filePath)).toBe(false)
    expect(calls.shell).not.toHaveBeenCalled()
    expect(calls.read).not.toHaveBeenCalled()
    expect(calls.edit).not.toHaveBeenCalled()
  })
})

describe('native file sequence paths', () => {
  it.runIf(existsSync('/bin/sh'))('preserves the original basename inside a literal private directory', () => {
    const workingDir = createNativeToolDirectory(directory)
    const sequence = nativeFileEditSequence(AgentProvider.CODEBUDDY, { workingDir, fileName: 'parity.ts' })
    const command: unknown = calls.shell.mock.calls[0]?.[2]
    if (typeof command !== 'string')
      throw new Error('The native file seed supplied no shell command.')
    // A native shell starts in the agent directory, before the supplied child directory.
    execFileSync('/bin/sh', ['-c', command], { cwd: directory })
    expect(sequence.filePath).toBe(join(workingDir, 'parity.ts'))
    expect(readFileSync(sequence.filePath, 'utf8')).toBe('const parityBefore = 1\n')
    expect(command).toContain(quotePosixShellArgument(sequence.filePath))
    expect(calls.read).toHaveBeenCalledWith(AgentProvider.CODEBUDDY, 'read-file', sequence.filePath)
    expect(calls.edit).toHaveBeenCalledWith(AgentProvider.CODEBUDDY, 'edit-file', { path: sequence.filePath, before: 'const parityBefore = 1', after: 'const parityAfter = 2' })
    const writeSequence = nativeFileWriteSequence(AgentProvider.CODEBUDDY, { workingDir, fileName: 'written.txt' })
    expect(calls.write).toHaveBeenCalledWith(AgentProvider.CODEBUDDY, 'write-file', { path: writeSequence.filePath, content: 'written-42\n' })
    expect(existsSync(writeSequence.filePath)).toBe(false)
    expect(existsSync(join(directory, 'command-expanded-marker'))).toBe(false)
  })

  it('rejects relative directories and filename traversal before a tool is built', () => {
    for (const build of [nativeFileEditSequence, nativeFileWriteSequence]) {
      for (const workingDir of ['', '.', 'relative/path'])
        expect(() => build(AgentProvider.CODEBUDDY, { workingDir, fileName: 'file.txt' })).toThrow('absolute private working directory')
      for (const fileName of ['', '.', '..', '../escape', 'nested/file', 'nested\\file', '\0'])
        expect(() => build(AgentProvider.CODEBUDDY, { workingDir: directory, fileName })).toThrow('one filename component')
    }
    for (const call of [calls.shell, calls.read, calls.edit, calls.write])
      expect(call).not.toHaveBeenCalled()
  })
})
