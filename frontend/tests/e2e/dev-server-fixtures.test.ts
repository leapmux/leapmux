import type { DedicatedServer } from './dev-server-fixtures'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { devServerTest } from './dev-server-fixtures'
import { modelScriptFixtures } from './helpers/modelScriptFixture'

// `test.extend` of Playwright returns a test object whose fixtures no API reads back, so the mock records the
// fixtures that `devServerTest` passes to it.
const extend = vi.hoisted(() => ({ fixtures: [] as Array<Record<string, unknown>> }))

vi.mock('@playwright/test', async (original) => {
  const actual = await original<typeof import('@playwright/test')>()
  const test = new Proxy(actual.test, {
    get: (target, property, receiver) => property === 'extend'
      ? (fixtures: Record<string, unknown>) => {
          extend.fixtures.push(fixtures)
          return target
        }
      : Reflect.get(target, property, receiver),
  })
  return { ...actual, test }
})

type FixtureBody<T> = (args: Record<string, unknown>, use: (value: T) => Promise<void>) => Promise<void>

/** Build a test base from `start`, and return the fixture `name` that it registers. */
function registeredFixture<T>(start: (use: (server: DedicatedServer) => Promise<void>) => Promise<void>, name: string): FixtureBody<T> {
  devServerTest(start)
  const fixture = extend.fixtures.at(-1)?.[name]
  if (typeof fixture !== 'function')
    throw new Error(`devServerTest registers no fixture function ${name}.`)
  return fixture as FixtureBody<T>
}

const SERVER: DedicatedServer = { hubUrl: 'http://127.0.0.1:4100' }

beforeEach(() => {
  extend.fixtures.length = 0
})

describe('devServerTest', () => {
  it('registers the test deadline and the model script that the other test bases also use', () => {
    devServerTest(async use => use(SERVER))
    expect(extend.fixtures).toHaveLength(1)
    expect(extend.fixtures[0]?.testStartedAt).toBe(modelScriptFixtures.testStartedAt)
    expect(extend.fixtures[0]?.modelScript).toBe(modelScriptFixtures.modelScript)
  })

  it('runs the test between the start and the stop of its own server', async () => {
    const events: string[] = []
    const server = registeredFixture<DedicatedServer>(async (use) => {
      events.push('start')
      await use(SERVER)
      events.push('stop')
    }, 'server')
    await server({}, async (value) => {
      events.push(`test at ${value.hubUrl}`)
    })
    expect(events).toEqual(['start', `test at ${SERVER.hubUrl}`, 'stop'])
  })

  it('fails the test when the server does not start', async () => {
    const failure = new Error('The dev server did not start.')
    const server = registeredFixture<DedicatedServer>(async () => {
      throw failure
    }, 'server')
    const use = vi.fn(async () => {})
    await expect(server({}, use)).rejects.toBe(failure)
    expect(use).not.toHaveBeenCalled()
  })

  it('points the base URL of the page at the hub of the server', async () => {
    const baseURL = registeredFixture<string>(async use => use(SERVER), 'baseURL')
    const use = vi.fn(async (_url: string) => {})
    await baseURL({ server: { hubUrl: 'http://127.0.0.1:4200' } }, use)
    expect(use).toHaveBeenCalledExactlyOnceWith('http://127.0.0.1:4200')
  })
})
