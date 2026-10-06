import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { createMiMoControlDeletion } from './controlScenarios'

const native = vi.hoisted(() => ({ currentAgent: vi.fn() }))
vi.mock('../helpers/nativeScenario', async (importOriginal) => {
  const original = await importOriginal<typeof import('../helpers/nativeScenario')>()
  return { ...original, currentNativeAgent: native.currentAgent }
})

const scratchRoot = resolve(process.cwd(), '../.tmp')
const context: ManagedNativeScenarioContext = {
  provider: AgentProvider.MIMO_CODE,
  providerAgent: { provider: AgentProvider.MIMO_CODE, prefix: 'native-e2e' },
  workspaceId: 'mimo-control-unit',
  leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
  get page(): Page { throw new Error('Planning the MiMo control deletion must not access the browser.') },
  get modelScript(): ModelScript { throw new Error('Planning the MiMo control deletion must not access the model.') },
}
let workingDir: string

beforeEach(() => {
  vi.clearAllMocks()
  mkdirSync(scratchRoot, { recursive: true })
  workingDir = mkdtempSync(join(scratchRoot, 'mimo-control-unit-'))
  native.currentAgent.mockResolvedValue({ workingDir })
})
afterEach(() => rmSync(workingDir, { recursive: true, force: true }))

function commandOf(plan: Awaited<ReturnType<typeof createMiMoControlDeletion>>): string {
  const command = plan.toolCall.arguments?.command
  if (typeof command !== 'string')
    throw new Error('The planned MiMo control deletion holds no shell command.')
  return command
}

function resultRecord(callId: string, output: string): MockModelRequestRecord {
  return { protocol: 'openai-chat-completions', path: '/chat/completions', body: { messages: [{ role: 'tool', tool_call_id: callId, content: output }] } }
}

describe('createMiMoControlDeletion', () => {
  it('holds the planned command with an output gate', async () => {
    const plan = await createMiMoControlDeletion(context, 'editor')
    expect(plan.outputGate).toBeDefined()
    expect(commandOf(plan)).toContain(plan.outputGate!.gate.releasePath)
  })

  it('keeps the gate file inside the private working directory of the agent', async () => {
    const plan = await createMiMoControlDeletion(context, 'editor')
    expect(plan.outputGate!.gate.releasePath.startsWith(workingDir)).toBe(true)
    expect(existsSync(plan.outputGate!.gate.releasePath)).toBe(false)
  })

  it('names each scratch file and call after its purpose', async () => {
    const editor = await createMiMoControlDeletion(context, 'editor')
    const trust = await createMiMoControlDeletion(context, 'workspace-trust')
    expect(editor.toolCall.id).toBe('native-editor-permission')
    expect(trust.toolCall.id).toBe('native-workspace-trust-permission')
    expect(existsSync(join(workingDir, 'native-editor-control.txt'))).toBe(true)
    expect(existsSync(join(workingDir, 'native-workspace-trust-control.txt'))).toBe(true)
  })

  it('creates the plan without access to the browser', async () => {
    await expect(createMiMoControlDeletion(context, 'editor')).resolves.toBeDefined()
  })

  it('rejects an agent without a working directory', async () => {
    native.currentAgent.mockResolvedValue({ workingDir: '' })
    await expect(createMiMoControlDeletion(context, 'editor')).rejects.toThrow('requires a working directory')
  })

  it.runIf(existsSync('/bin/sh'))('deletes the file and prints the output, and ends only after the gate releases', async () => {
    const plan = await createMiMoControlDeletion(context, 'editor')
    const file = join(workingDir, 'native-editor-control.txt')
    await plan.beforeDecision()
    const child = spawn('/bin/sh', ['-c', commandOf(plan)], { cwd: workingDir, stdio: ['ignore', 'pipe', 'pipe'] })
    try {
      let stdout = ''
      child.stdout.on('data', chunk => stdout += String(chunk))
      const closed = new Promise<number | null>(resolveClose => child.on('close', code => resolveClose(code)))
      await vi.waitFor(() => expect(stdout).toBe('NATIVECONTROL42\n'), { timeout: 30_000, interval: 5 })
      // The command printed its output, so the deletion before it ran too.
      expect(existsSync(file)).toBe(false)
      expect(child.exitCode).toBeNull()
      plan.outputGate!.gate.release()
      expect(await closed).toBe(0)
      await plan.nativeProof(resultRecord(plan.toolCall.id, stdout))
    }
    finally {
      child.kill('SIGKILL')
    }
  })

  it('refuses a proof when the file still exists', async () => {
    const plan = await createMiMoControlDeletion(context, 'editor')
    // The proof throws at once or rejects, whichever the plan type allows.
    await expect((async () => plan.nativeProof(resultRecord(plan.toolCall.id, 'NATIVECONTROL42\n')))()).rejects.toThrow()
  })
})
