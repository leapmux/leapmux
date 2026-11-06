import { resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'
import { downloadGoModules } from './download-go-modules.mjs'

describe('downloadGoModules', () => {
  it('populates workspace and isolated tool modules in the same payload cache', () => {
    const calls = []
    const env = { GOMODCACHE: '/payload', GOWORK: '/checkout/go.work' }
    expect(downloadGoModules({
      cwd: '/checkout',
      env,
      run: (command, args, options) => {
        calls.push({ command, args, options })
        return { status: 0 }
      },
    })).toBe(0)
    expect(calls.map(call => call.options.cwd)).toEqual([
      '/checkout',
      ...['golangci-lint', 'sqlc', 'gotestsum'].map(name => resolve('/checkout/tools', name)),
    ])
    for (const [index, call] of calls.entries()) {
      expect(call.command).toBe('go')
      expect(call.args).toEqual(['mod', 'download'])
      expect(call.options.env.GOMODCACHE).toBe('/payload')
      expect(call.options.env.GOWORK).toBe(index === 0
        ? '/checkout/go.work'
        : 'off')
    }
    expect(env.GOWORK).toBe('/checkout/go.work')
  })

  it('stops immediately when a download fails or cannot start', () => {
    for (const failureIndex of [0, 1, 2, 3]) {
      let calls = 0
      expect(downloadGoModules({
        run: () => ({
          status: calls++ === failureIndex
            ? 7
            : 0,
        }),
      })).toBe(7)
      expect(calls).toBe(failureIndex + 1)
    }
    expect(() => downloadGoModules({
      run: () => ({
        error: new Error('cannot start'),
      }),
    })).toThrow('cannot start')
  })
})
