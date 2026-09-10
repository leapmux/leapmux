import { execFileSync } from 'node:child_process'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { resolveTaskBin } from './resolve-task-bin'

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  const execFileSync = vi.fn()
  return { ...actual, execFileSync, default: { ...actual, execFileSync } }
})

beforeEach(() => {
  vi.mocked(execFileSync).mockReset()
})

describe('task executable lookup', () => {
  it('works without the Unix which command', () => {
    vi.mocked(execFileSync).mockImplementation((command) => {
      if (command !== 'task')
        throw new Error('executable not found')
      return ''
    })
    expect(resolveTaskBin()).toBe('task')
    expect(execFileSync).toHaveBeenCalledExactlyOnceWith('task', ['--version'], expect.any(Object))
  })

  it('tries go-task when task is unavailable', () => {
    vi.mocked(execFileSync).mockImplementation((command) => {
      if (command !== 'go-task')
        throw new Error('executable not found')
      return ''
    })
    expect(resolveTaskBin()).toBe('go-task')
  })

  it('reports that neither executable is usable', () => {
    vi.mocked(execFileSync).mockImplementation(() => {
      throw new Error('executable not found')
    })
    expect(() => resolveTaskBin()).toThrow('Neither "task" nor "go-task"')
    expect(execFileSync).toHaveBeenCalledTimes(2)
  })
})
