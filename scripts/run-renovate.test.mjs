import process from 'node:process'
import { describe, expect, it } from 'bun:test'
import { runWithTimeout } from './run-renovate.mjs'

describe('runWithTimeout', () => {
  it('preserves successful and failed CLI exit codes', async () => {
    for (const code of [0, 17]) {
      const result = await runWithTimeout(process.execPath, ['-e', `process.exit(${code})`], { stdio: 'ignore' })
      expect(result.code).toBe(code)
      expect(result.timedOut).toBe(false)
    }
  })
  it('kills an execution that exceeds its deadline and returns a failure', async () => {
    const result = await runWithTimeout(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { timeoutMs: 100, stdio: 'ignore' })
    expect(result.code).toBe(124)
    expect(result.timedOut).toBe(true)
  })
  it('rejects invalid deadlines and unavailable executables', async () => {
    for (const timeoutMs of [0, -1, NaN, Infinity, 1.5])
      await expect(runWithTimeout('unused', [], { timeoutMs })).rejects.toThrow()
    await expect(runWithTimeout('leapmux-no-such-command', [], { stdio: 'ignore' })).rejects.toThrow()
  })
  it('rejects a timeout that exceeds the timer maximum', async () => {
    await expect(runWithTimeout('unused', [], { timeoutMs: 2147483648, stdio: 'ignore' })).rejects.toThrow('timeout must')
  })
  it('reports a failed diagnostic write and stops the execution', async () => {
    await expect(runWithTimeout(process.execPath, ['-e', 'console.log("fixture output"); setInterval(() => {}, 1000)'], {
      stdio: ['ignore', 'pipe', 'pipe'],
      output: { write: () => { throw new Error('diagnostic write failed') } },
    })).rejects.toThrow('diagnostic write failed')
  })
})
