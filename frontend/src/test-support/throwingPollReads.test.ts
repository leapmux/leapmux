import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles, e2eRoot } from '~/test-support/e2eFiles'
import { posixRelative } from '~/test-support/sourceTree'
import { throwingPollReads } from '~/test-support/throwingPollReads'

// E2E guard: no `expect.poll` may wait on a Worker or Hub read. Playwright calls the poll function outside the `try`
// that retries a failed matcher, so the first read that throws -- a Worker read while the Worker reconnects -- ends the
// poll at once. `retryUntilPass` in `tests/e2e/helpers/retryUntilPass.ts` retries such a read. `throwingPollReads.ts`
// holds the analysis.

/**
 * The polls that the guard accepts for now, by file and enclosing function, each with its reason. An entry that
 * matches no poll fails the guard, so a converted poll takes its entry with it.
 */
const ACCEPTED: ReadonlyMap<string, string> = new Map([
  [
    'amp/nativeCatalog.ts closeAmpCatalogAgent',
    'This poll needs the same conversion. A change to this file on a parallel branch is not merged yet, so this branch '
    + 'leaves the file unchanged. Convert the poll to `retryUntilPass` and delete this entry.',
  ],
])

/** Analyze sources keyed by a path below a fixed root. */
function analyze(files: Record<string, string>) {
  return throwingPollReads(Object.entries(files).map(([path, source]) => ({ path: `/e2e/${path}`, source })))
}

describe('throwingPollReads', () => {
  it('finds a Worker read that a poll function reaches through an imported helper', () => {
    const findings = analyze({
      'helpers/api.ts': 'export async function getTestChannel() { return channel }',
      'helpers/read.ts': `import { getTestChannel } from './api'
export async function readAgent() { return (await getTestChannel()).agent }`,
      'spec.ts': `import { readAgent } from './helpers/read'
async function check() {
  await expect.poll(async () => (await readAgent()).status).toBe(1)
}`,
    })
    expect(findings).toEqual([{ path: '/e2e/spec.ts', line: 3, enclosingFunction: 'check', chain: ['readAgent', 'getTestChannel'] }])
  })

  it('finds a channel method call and a Hub call in the poll function itself', () => {
    const findings = analyze({
      'a.ts': 'await expect.poll(async () => (await channel.callWorker(id)).paused).toBe(true)',
      'b.ts': 'await expect.poll(() => callHub(url)).toBe(true)',
    })
    expect(findings.map(finding => finding.chain)).toEqual([['callWorker'], ['callHub']])
  })

  it('resolves a poll function that is a named local function', () => {
    const findings = analyze({
      'spec.ts': `const readQueue = async () => channel.callWorker('ListAgentInputQueue')
await expect.poll(readQueue).toBe(true)`,
    })
    expect(findings.map(finding => finding.chain)).toEqual([['readQueue', 'callWorker']])
  })

  it('accepts a read that a catch clause turns into a value, and a read whose promise ends in a catch call', () => {
    const findings = analyze({
      'spec.ts': `async function tolerant() {
  try {
    return await getTestChannel()
  }
  catch {
    return null
  }
}
await expect.poll(tolerant).toBe(null)
await expect.poll(() => getTestChannel().catch(() => null)).toBe(null)`,
    })
    expect(findings).toEqual([])
  })

  it('finds a read whose catch clause throws again', () => {
    const findings = analyze({
      'spec.ts': `async function rethrows() {
  try {
    return await getTestChannel()
  }
  catch (error) {
    throw new Error('wrapped', { cause: error })
  }
}
await expect.poll(rethrows).toBe(null)`,
    })
    expect(findings.map(finding => finding.chain)).toEqual([['rethrows', 'getTestChannel']])
  })

  it('accepts a poll over a read that reaches no transport call, such as the mock model status', () => {
    const findings = analyze({
      'spec.ts': `const status = async () => fetch(url)
await expect.poll(async () => (await modelScript.status()).nextStep).toBe(1)
await expect.poll(status).toBe(1)`,
    })
    expect(findings).toEqual([])
  })

  it('ends the search at a recursive function, and still finds its transport call', () => {
    const findings = analyze({
      'spec.ts': `async function walk(depth) {
  if (depth > 0)
    return walk(depth - 1)
  return hubRequest(url)
}
await expect.poll(() => walk(2)).toBe(1)`,
    })
    expect(findings.map(finding => finding.chain)).toEqual([['walk', 'hubRequest']])
  })

  it('ignores a call that is not expect.poll', () => {
    expect(analyze({ 'spec.ts': 'await other.poll(() => getTestChannel()).toBe(1)' })).toEqual([])
  })
})

describe('e2e polls over Worker and Hub reads', () => {
  const files = collectE2EFiles().map(path => ({ path, source: readFileSync(path, 'utf-8') }))
  const findings = throwingPollReads(files)
  const key = (finding: { path: string, enclosingFunction: string }) => `${posixRelative(e2eRoot, finding.path)} ${finding.enclosingFunction}`

  it('scans the e2e tree', () => {
    expect(files.length).toBeGreaterThan(100)
  })

  it('never waits on a Worker or Hub read through expect.poll', () => {
    const offenders = findings
      .filter(finding => !ACCEPTED.has(key(finding)))
      .map(finding => `${posixRelative(e2eRoot, finding.path)}:${finding.line}  ${finding.chain.join(' -> ')}`)
    const hint = [
      'A Worker or Hub read throws while the Worker reconnects, and expect.poll ends at the first read that throws.',
      'Wait with retryUntilPass (tests/e2e/helpers/retryUntilPass.ts), and put the assertion inside the attempt:',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })

  it('keeps no accepted poll that the e2e tree no longer holds', () => {
    const found = new Set(findings.map(key))
    expect([...ACCEPTED.keys()].filter(entry => !found.has(entry))).toEqual([])
  })
})
