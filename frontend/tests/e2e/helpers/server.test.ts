import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { hubSpawnEnv } from './server'

describe('hubSpawnEnv', () => {
  it('clears the dev-frontend URL the ambient environment carries', () => {
    const before = process.env.LEAPMUX_HUB_DEV_FRONTEND
    process.env.LEAPMUX_HUB_DEV_FRONTEND = 'http://localhost:5173'
    try {
      expect(hubSpawnEnv().LEAPMUX_HUB_DEV_FRONTEND).toBeUndefined()
    }
    finally {
      if (before === undefined)
        delete process.env.LEAPMUX_HUB_DEV_FRONTEND
      else
        process.env.LEAPMUX_HUB_DEV_FRONTEND = before
    }
  })

  it('keeps the variables a caller layers on top', () => {
    const env = hubSpawnEnv({ LEAPMUX_WORKER_NAME: 'Local' })
    expect(env.LEAPMUX_WORKER_NAME).toBe('Local')
  })

  // The guard sits after the spread, so a caller cannot reinstate the value.
  // The parameter type omits the key as well, so this only reaches the runtime
  // through a cast -- which is exactly the accident worth pinning, because a
  // silently honoured override is the same defect the helper exists to remove.
  it('cannot be overridden by its own caller', () => {
    const env = hubSpawnEnv({ LEAPMUX_HUB_DEV_FRONTEND: 'http://localhost:5173' } as Record<string, string>)
    expect(env.LEAPMUX_HUB_DEV_FRONTEND).toBeUndefined()
  })
})

// A hub that inherits `LEAPMUX_HUB_DEV_FRONTEND` serves whatever checkout a
// developer's Vite server runs from, so the spec asserts against a frontend
// nobody built from the code under test -- and it can PASS that way, which is
// the worse half. `hubSpawnEnv` is the one home for that guard, and this test
// is what keeps it the only one: three hub spawns bypassed it for a whole
// commit whose subject said "every spawned hub".
describe('every hub spawn goes through hubSpawnEnv', () => {
  const e2eRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')

  const walk = (dir: string): string[] =>
    readdirSync(dir).flatMap((entry) => {
      const path = join(dir, entry)
      if (statSync(path).isDirectory())
        return entry === 'node_modules' ? [] : walk(path)
      return path.endsWith('.ts') ? [path] : []
    })

  it('spawns no hub with a hand-rolled environment', () => {
    const offenders: string[] = []
    for (const file of walk(e2eRoot)) {
      if (file.endsWith('server.test.ts'))
        continue
      const source = readFileSync(file, 'utf8')
      // Each `spawn(...)` call, with the arguments that follow it up to the
      // closing brace of its options object. A hub spawn is one whose argument
      // list names the `hub`, `solo` or `dev` subcommand.
      for (const call of source.split('spawn(').slice(1)) {
        const block = call.slice(0, call.indexOf('})'))
        const isHub = /['"](?:hub|solo|dev)['"]/.test(block)
        if (isHub && block.includes('env:') && !block.includes('hubSpawnEnv'))
          offenders.push(`${file.slice(e2eRoot.length + 1)}: ${block.split('\n').find(l => l.includes('env:'))?.trim()}`)
      }
    }
    expect(offenders, 'route every hub spawn through hubSpawnEnv').toEqual([])
  })
})
