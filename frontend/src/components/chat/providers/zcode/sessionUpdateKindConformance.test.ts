import type { ZCodeSessionUpdateKind } from './sessionUpdateKind'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { zcodeSessionUpdateKind } from './sessionUpdateKind'

/**
 * The browser's half of testdata/zcode_session_update_kind_conformance.json.
 *
 * `TestZCodeSessionUpdateKindConformance` in the worker replays the same file
 * against `zcodeSessionUpdatedPayload.kind`. A divergence between the two
 * copies classifies a stored row differently on the two sides: the worker
 * persists one kind of notification while the browser draws another.
 */
const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/zcode_session_update_kind_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, payload: Record<string, unknown>, kind: ZCodeSessionUpdateKind }>
}

/** The kinds in the vocabulary, one entry for each. A new kind joins it at compile time. */
const EVERY_KIND: Record<ZCodeSessionUpdateKind, true> = {
  drop: true,
  networkStatus: true,
  backgroundTask: true,
  subagent: true,
  modelResponse: true,
  goalVerification: true,
  goalChange: true,
  modeChange: true,
  compaction: true,
  microcompact: true,
  modelSelected: true,
  hookRun: true,
  hookAdmission: true,
  steerDiscarded: true,
}

describe('zcode session update kind conformance', () => {
  /**
   * Every kind in the vocabulary needs a corpus witness. A corpus that loses one
   * lets a divergence on that kind pass both suites: the worker persists one kind
   * of notification while the browser draws another.
   */
  it('witnesses every kind in the vocabulary', () => {
    for (const kind of Object.keys(EVERY_KIND) as ZCodeSessionUpdateKind[]) {
      expect(
        fixture.cases.some(c => c.kind === kind),
        `no case pins the ${kind} kind`,
      ).toBe(true)
    }
  })

  it.each(fixture.cases)('$name', ({ payload, kind }) => {
    const before = JSON.stringify(payload)
    expect(zcodeSessionUpdateKind(payload)).toBe(kind)
    expect(JSON.stringify(payload)).toBe(before)
  })
})
