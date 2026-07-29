import type { CrdtOp } from '~/generated/leapmux/v1/user_ops_pb'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { fromJson } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { CrdtOpSchema } from '~/generated/leapmux/v1/user_ops_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { applyOp, newState } from './apply'
import { project } from './project'

/**
 * This file is one half of a cross-language contract. The other half is
 * `backend/internal/hub/crdt/conformance_test.go`, and both read
 * `testdata/crdt_projection_conformance.json` — read that file's `_readme`
 * first.
 *
 * `apply.ts` and `project.ts` re-implement the hub's merge and tab-projection
 * rules, because this client has to project optimistically before the hub has
 * seen an op. Two hand-written implementations of one specification drift, and
 * these did: the sides came to disagree about which tile a collapsed split's
 * tabs report, with both suites green throughout. A shared fixture is what
 * makes that visible — change the rules on one side only, and the other side's
 * suite fails on the same case.
 *
 * If a case here goes red, "update the expectation" is almost never the fix.
 */

/**
 * Resolved from this file rather than the CWD: vitest's working directory is
 * not part of the contract, and the fixture lives at the repo root (outside
 * `src`, so outside tsconfig's `include` and vite's root — which is why this is
 * a runtime read rather than a static JSON import). Mirrors
 * `src/lib/ipAddress.test.ts`.
 */
const testdataDir = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../testdata')

/** Curated: every case is named for the scenario it pins and is worth reading. */
const fixturePath = resolve(testdataDir, 'crdt_projection_conformance.json')
/**
 * Generated: coverage rather than documentation. Its expectations were recorded
 * by the GO projection, so replaying it here is what turns a rule this side
 * gains (or loses) into a red suite, on op logs nobody had to think to write
 * down. Regenerate deliberately with `task generate-conformance-corpus`.
 */
const corpusPath = resolve(testdataDir, 'crdt_projection_corpus.json')

/** The projected row as the fixture spells it. */
interface ConformanceTab {
  tabType: string
  tabId: string
  workspaceId: string
  tileId: string
  workerId: string
  position: string
}

interface ConformanceCase {
  name: string
  why: string
  ops: unknown[]
  expect: { owned: ConformanceTab[], rendered: ConformanceTab[] }
}

interface ConformanceFixture {
  userId: string
  cases: ConformanceCase[]
}

function loadFixture(fixturePath: string): ConformanceFixture {
  const parsed = JSON.parse(readFileSync(fixturePath, 'utf8')) as ConformanceFixture

  // A fixture that silently loads nothing would make this suite pass while
  // asserting nothing — the one failure mode a shared fixture must not have.
  if (!parsed.cases?.length)
    throw new Error(`${fixturePath} loaded no cases; it is shared with backend/internal/hub/crdt/conformance_test.go`)
  if (!parsed.userId)
    throw new Error(`${fixturePath} has no userId`)

  // And the version of that failure specific to THIS fixture: a case whose
  // `ops` key is misspelled parses as an empty op log, produces an empty
  // projection, and matches an `expect` the same typo left empty. Both suites
  // would then agree on nothing, in perfect sync.
  const seen = new Set<string>()
  parsed.cases.forEach((c, i) => {
    if (!c.name)
      throw new Error(`case ${i} has no name`)
    if (seen.has(c.name))
      throw new Error(`duplicate case name "${c.name}"`)
    seen.add(c.name)
    if (!c.ops?.length)
      throw new Error(`case "${c.name}" has no ops -- check the key spelling`)
  })
  return parsed
}

/** Proto enum number → the name protojson emits, so both sides compare strings. */
const TAB_TYPE_NAMES: Record<number, string> = {
  [TabType.UNSPECIFIED]: 'TAB_TYPE_UNSPECIFIED',
  [TabType.AGENT]: 'TAB_TYPE_AGENT',
  [TabType.TERMINAL]: 'TAB_TYPE_TERMINAL',
  [TabType.FILE]: 'TAB_TYPE_FILE',
}

function projectedTabs(tabs: ReadonlyArray<{
  tabType: TabType
  tabId: string
  workspaceId: string
  tileId: string
  workerId: string
  position: string
}>): ConformanceTab[] {
  return tabs.map(t => ({
    tabType: TAB_TYPE_NAMES[t.tabType] ?? `TAB_TYPE_${t.tabType}`,
    tabId: t.tabId,
    workspaceId: t.workspaceId,
    tileId: t.tileId,
    workerId: t.workerId,
    position: t.position,
  }))
}

function replayConformance(label: string, fixture: ConformanceFixture): void {
  describe(label, () => {
    for (const c of fixture.cases) {
      it(c.name, () => {
        const state = newState(fixture.userId)
        c.ops.forEach((rawOp, i) => {
          let op: CrdtOp
          try {
            op = fromJson(CrdtOpSchema, rawOp as never)
          }
          catch (err) {
            throw new Error(`case "${c.name}" op ${i} is not valid protobuf JSON: ${String(err)}`)
          }
          // Both Apply implementations read canonical_hlc and silently no-op
          // without it, so an op missing one would assert nothing on both sides
          // at once.
          if (!op.canonicalHlc)
            throw new Error(`case "${c.name}" op ${i} has no canonicalHlc, so applyOp would ignore it`)
          applyOp(state, op)
        })

        const proj = project(state)
        const owned = projectedTabs(proj.ownedTabs)
        const rendered = projectedTabs(proj.renderedTabs)

        // A case that expects rows must actually produce some; otherwise a
        // silently-dropped op log would satisfy an empty-vs-empty compare.
        if (c.expect.owned.length > 0)
          expect(owned, `expects owned tabs but the projection produced none (${c.why})`).not.toHaveLength(0)

        expect(owned, `owned tabs disagree with the shared fixture (${c.why})`).toEqual(c.expect.owned)
        expect(rendered, `rendered tabs disagree with the shared fixture (${c.why})`).toEqual(c.expect.rendered)
      })
    }
  })
}

replayConformance('crdt projection conformance (shared with the hub)', loadFixture(fixturePath))
replayConformance('crdt projection conformance corpus (generated, shared with the hub)', loadFixture(corpusPath))
