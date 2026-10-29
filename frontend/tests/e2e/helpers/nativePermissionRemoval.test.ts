import type { ManagedNativeScenarioContext } from './nativeScenario'
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { createNativePermissionRemoval } from './nativePermissionRemoval'
import { currentNativeAgent, nativeToolOutcome } from './nativeScenario'
import { getGlobalState } from './server'

vi.mock('./nativeScenario', () => ({
  currentNativeAgent: vi.fn(),
  nativeToolOutcome: vi.fn(),
}))
vi.mock('./server', () => ({
  getGlobalState: vi.fn(),
}))

let scratch: string
let workingDir: string
const context = { provider: AgentProvider.CLAUDE_CODE } as ManagedNativeScenarioContext

beforeEach(() => {
  vi.clearAllMocks()
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
  mkdirSync(root, { recursive: true })
  scratch = mkdtempSync(join(root, 'permission-removal-unit-'))
  workingDir = join(scratch, 'private workspace')
  mkdirSync(workingDir)
  vi.mocked(currentNativeAgent).mockResolvedValue({ workingDir } as Awaited<ReturnType<typeof currentNativeAgent>>)
  vi.mocked(getGlobalState).mockReturnValue({ tmpDir: scratch } as ReturnType<typeof getGlobalState>)
})
afterEach(() => rmSync(scratch, { recursive: true, force: true }))

describe('createNativePermissionRemoval', () => {
  it('creates a private file and requires both native output and actual removal', async () => {
    const operation = await createNativePermissionRemoval(context)
    await operation.beforeDecision()
    expect(readdirSync(workingDir)).toHaveLength(1)
    const path = join(workingDir, readdirSync(workingDir)[0]!)
    expect(readFileSync(path, 'utf8')).toContain('Keep this file')
    const request = {} as Parameters<typeof operation.nativeProof>[0]
    vi.mocked(nativeToolOutcome).mockResolvedValue({ text: `${operation.toolCall.id}42` })
    await expect(operation.nativeProof(request)).rejects.toThrow('the native command must remove its private file')
    const command = operation.toolCall.arguments?.command
    expect(typeof command).toBe('string')
    expect(command).toContain(path)
    rmSync(path)
    vi.mocked(nativeToolOutcome).mockResolvedValue({ text: 'The command did not run.' })
    await expect(operation.nativeProof(request)).rejects.toThrow('the native command must report its removal marker')
    vi.mocked(nativeToolOutcome).mockResolvedValue({ text: `${operation.toolCall.id}42` })
    await operation.nativeProof(request)
  })

  it('refuses a directory outside the private run before writing a file', async () => {
    vi.mocked(getGlobalState).mockReturnValue({ tmpDir: workingDir } as ReturnType<typeof getGlobalState>)
    vi.mocked(currentNativeAgent).mockResolvedValue({ workingDir: scratch } as Awaited<ReturnType<typeof currentNativeAgent>>)
    await expect(createNativePermissionRemoval(context)).rejects.toThrow('outside the E2E run')
    expect(readdirSync(scratch)).toEqual(['private workspace'])
  })
})
