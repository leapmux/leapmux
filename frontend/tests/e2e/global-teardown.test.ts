import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import globalTeardown from './global-teardown'

let scratch: string

vi.mock('node:os', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:os')>()
  const tmpdir = () => scratch
  return { ...actual, tmpdir, default: { ...actual, tmpdir } }
})

beforeEach(() => {
  const root = resolve(import.meta.dirname, '../../..', '.tmp')
  mkdirSync(root, { recursive: true })
  scratch = mkdtempSync(join(root, 'teardown-test-'))
  vi.stubEnv('E2E_STATE_PATH', '')
})

afterEach(() => {
  rmSync(scratch, { recursive: true, force: true })
  vi.unstubAllEnvs()
})

describe('end-to-end teardown ownership', () => {
  it('preserves fixture directories from another run', async () => {
    const own = join(scratch, 'own-run')
    const other = join(scratch, 'leapmux-e2e-separate-other')
    mkdirSync(own)
    mkdirSync(other)
    writeFileSync(join(other, 'pids.json'), '[]')
    const statePath = join(own, 'e2e-state.json')
    writeFileSync(statePath, JSON.stringify({ tmpDir: own, binaryPath: 'unused' }))
    vi.stubEnv('E2E_STATE_PATH', statePath)
    await globalTeardown()
    expect(existsSync(other)).toBe(true)
  })

  it('does not select another run when its own state is absent', async () => {
    const other = join(scratch, 'leapmux-e2e-other')
    mkdirSync(other)
    writeFileSync(join(other, 'e2e-state.json'), JSON.stringify({ tmpDir: other, binaryPath: 'unused' }))
    await globalTeardown()
    expect(existsSync(other)).toBe(true)
  })
})
