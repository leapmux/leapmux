import { execFileSync } from 'node:child_process'
import process from 'node:process'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { resolveTaskBin } from './resolve-task-bin'

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  const execFileSync = vi.fn()
  return {
    ...actual,
    execFileSync,
    default: { ...actual, execFileSync },
  }
})

beforeEach(() => {
  vi.mocked(execFileSync).mockReset()
})

// The probe list resolveTaskBin walks, in order, on this platform: Windows
// names the suffixed executable first, and both spellings answer everywhere.
const probeOrder = process.platform === 'win32'
  ? [
      'task.exe',
      'go-task.exe',
      'task',
      'go-task',
    ]
  : ['task', 'go-task']

describe('task executable lookup', () => {
  it('answers the first name that runs', () => {
    vi.mocked(execFileSync).mockImplementation((command) => {
      if (command !== probeOrder[0])
        throw new Error('executable not found')
      return ''
    })
    expect(resolveTaskBin()).toBe(probeOrder[0])
    expect(execFileSync).toHaveBeenCalledExactlyOnceWith(probeOrder[0], ['--version'], expect.any(Object))
  })

  it('falls through to a later name when the earlier one is unavailable', () => {
    const usable = probeOrder[1]
    vi.mocked(execFileSync).mockImplementation((command) => {
      if (command !== usable)
        throw new Error('executable not found')
      return ''
    })
    expect(resolveTaskBin()).toBe(usable)
    expect(execFileSync).toHaveBeenCalledTimes(2)
  })

  it('reports that neither executable is usable', () => {
    vi.mocked(execFileSync).mockImplementation(() => {
      throw new Error('executable not found')
    })
    expect(() => resolveTaskBin()).toThrow('Neither "task" nor "go-task"')
    expect(execFileSync).toHaveBeenCalledTimes(probeOrder.length)
  })
})
