import { describe, expect, it, vi } from 'vitest'
import { cliSkipFixture } from './provider-fixture-factory'

describe('cliSkipFixture', () => {
  /** Run the fixture callback with a recording test info, as Playwright runs an automatic fixture. */
  async function run(reason: string | null) {
    const [fixture, options] = cliSkipFixture(reason)
    const events: string[] = []
    const testInfo = {
      skip: vi.fn((condition: boolean, description: string) => {
        events.push(`skip:${condition}:${description}`)
        // Playwright ends the test from inside `skip` with a thrown marker when the condition holds.
        if (condition)
          throw new Error(`skipped: ${description}`)
      }),
    }
    const use = vi.fn(async () => {
      events.push('use')
    })
    const outcome = await (fixture as unknown as (args: object, use: () => Promise<void>, info: typeof testInfo) => Promise<void>)({}, use, testInfo)
      .then(() => 'ran', (error: unknown) => (error as Error).message)
    return { options, events, outcome }
  }

  it('registers an automatic fixture, so it runs before the fixtures that start an agent', () => {
    expect(cliSkipFixture(null)[1]).toEqual({ auto: true })
  })

  it('skips with the reason before the test uses any fixture when the CLI is missing', async () => {
    const { events, outcome } = await run('Amp E2E requires the amp CLI on PATH')
    expect(events).toEqual(['skip:true:Amp E2E requires the amp CLI on PATH'])
    expect(outcome).toBe('skipped: Amp E2E requires the amp CLI on PATH')
  })

  it('runs the test when the CLI is present', async () => {
    const { events, outcome } = await run(null)
    expect(events).toEqual(['skip:false:', 'use'])
    expect(outcome).toBe('ran')
  })

  it.each(['', '  \n'])('refuses an empty reason, which would skip without saying why: %j', (reason) => {
    expect(() => cliSkipFixture(reason)).toThrow('needs the reason')
  })
})
