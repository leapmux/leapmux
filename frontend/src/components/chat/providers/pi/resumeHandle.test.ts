import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { piValidateResumeHandle } from './resumeHandle'
// Side-effect import: it REGISTERS the Pi plugin, so the wiring case below reads the
// rule back out of the registry.
import './plugin'

/**
 * The browser half of `testdata/pi_resume_handle_conformance.json`. The worker
 * suite (`TestPiResumeHandleConformance`) reads the same file, so a one-sided
 * edit to either implementation turns that side red. See the file's own
 * `_readme` for the contract and for the one asymmetry it allows.
 */
describe('piValidateResumeHandle conformance', () => {
  interface HandleSpec {
    head?: string
    text: string
    repeat?: number
    tail?: string
  }

  interface Verdict {
    valid: boolean
    refusal: string
  }

  interface HandleCase {
    input: HandleSpec
    browser: Verdict
    worker: { posix: Verdict, windows: Verdict }
    why: string
  }

  function buildHandle(spec: HandleSpec): string {
    return (spec.head ?? '') + spec.text.repeat(spec.repeat ?? 1) + (spec.tail ?? '')
  }

  // Each token maps to a substring of THIS side's message. The worker's
  // wording differs (it reports `path traversal not allowed` where this
  // reports the `..` it refuses), so each suite carries its own map and the
  // fixture stays language-neutral.
  const refusalMarkers: Record<string, string> = {
    too_long: 'must be at most',
    not_absolute: 'must be absolute',
    traversal: 'must not contain ".."',
    leading_hyphen: 'must not start with a hyphen',
    forbidden_character: 'contains invalid characters',
    invisible_character: 'contains invisible characters',
    whitespace_at_edge: 'must not start or end with whitespace',
  }

  const fixturePath = resolve(
    dirname(fileURLToPath(import.meta.url)),
    '../../../../../../testdata/pi_resume_handle_conformance.json',
  )

  const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as { cases: HandleCase[] }

  // A fixture that silently loads zero cases would make this block pass while
  // asserting nothing -- the one failure mode a shared fixture must not have.
  it('loads the shared fixture', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$why', (c) => {
    const got = piValidateResumeHandle(buildHandle(c.input))
    if (c.browser.valid) {
      expect(c.browser.refusal, `case "${c.why}" is valid, so its refusal must be empty`).toBe('')
      expect(got).toBeNull()
      return
    }
    expect(got, `case "${c.why}" must be refused`).not.toBeNull()
    const marker = refusalMarkers[c.browser.refusal]
    expect(marker, `case "${c.why}" carries an unknown refusal token "${c.browser.refusal}"`).toBeDefined()
    expect(got).toContain(marker)
  })

  // The asymmetry this rule is allowed to have, in the ONE direction that is
  // safe. The browser may accept what a worker refuses -- it does not know the
  // worker's host, so it leaves the reserved device names and the spelling of
  // "absolute" to the side that does. The reverse removes a legitimate resume
  // with no way to reach it: a value this field refuses never reaches a worker
  // to be judged. The Go suite asserts the same invariant.
  it.each(fixture.cases.filter(c => !c.browser.valid))('no worker accepts what the browser refuses: $why', (c) => {
    expect(c.worker.posix.valid).toBe(false)
    expect(c.worker.windows.valid).toBe(false)
  })
})

// The dispatch this rule exists to be reachable by. A provider's resume rule is
// its own -- the worker asks `Provider.ResolveResumeHandle` per provider -- so
// the browser must ask the plugin rather than branch on a flag in a shared lib.
describe('piValidateResumeHandle picks the rule by shape', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('is exposed on the plugin, so the resume field reaches it through the registry', () => {
    expect(plugin?.session?.validateResumeHandle).toBe(piValidateResumeHandle)
  })

  it('takes a session ID under the token rule', () => {
    expect(piValidateResumeHandle('018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b')).toBeNull()
    // The token rule's own guards still apply to that half.
    expect(piValidateResumeHandle('-dangerous')).toContain('hyphen')
  })

  it('takes a session file path under the path rule', () => {
    expect(piValidateResumeHandle('/Users/dev/.pi/agent/sessions/p/s.jsonl')).toBeNull()
    expect(piValidateResumeHandle('C:\\Users\\pi\\sessions\\s.jsonl')).toBeNull()
    // A backslash and a length past 128 bytes are exactly what the TOKEN rule
    // bans, and a real Pi session path carries both -- which is why one rule
    // could not serve both shapes.
    const realPath = `/Users/dev/.pi/agent/sessions/${'a'.repeat(140)}.jsonl`
    expect(realPath.length).toBeGreaterThan(128)
    expect(piValidateResumeHandle(realPath)).toBeNull()
    // The path shape keeps its OWN, larger cap.
    expect(piValidateResumeHandle(`/tmp/${'a'.repeat(1024)}.jsonl`)).toContain('at most')
  })

  it('reads the `.jsonl` suffix and a separator as Pi\'s resolver does', () => {
    // A bare file name is a PATH to Pi, and a relative one, so it is refused as
    // a path rather than accepted as a token.
    expect(piValidateResumeHandle('session.jsonl')).toContain('absolute')
    expect(piValidateResumeHandle('sessions/s.jsonl')).toContain('absolute')
    // A bare tilde holds no separator, so BOTH sides read it as an ID.
    expect(piValidateResumeHandle('~')).toBeNull()
  })

  it('accepts the empty handle, which means no resume', () => {
    expect(piValidateResumeHandle('')).toBeNull()
  })
})
