import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { vi } from 'vitest'

/** A controllable process handle that sends no operating-system signals. */
export function createProcessStub(options: Partial<Pick<ChildProcess, 'pid' | 'exitCode' | 'signalCode'>> = {}) {
  const emitter = Object.assign(new EventEmitter(), {
    pid: 123 as number | undefined,
    exitCode: null as number | null,
    signalCode: null as NodeJS.Signals | null,
    kill: vi.fn<ChildProcess['kill']>(() => true),
    ...options,
  })
  return { emitter, proc: emitter as unknown as ChildProcess }
}
