import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'bun:test'
import { computeCaseId } from './agent-preservation-lib.mjs'
import { auditPreservation } from './audit-agent-case-preservation.mjs'

/** A minimal discovery manifest holding the cases the fixtures name. */
function discoveryOf(cases) {
  return { suites: cases.map(entry => ({
    title: entry.file,
    file: entry.file,
    specs: [{ file: entry.file, line: 1, title: entry.title, id: entry.id }],
  })) }
}

function caseOf(file, title, describe = []) {
  return { file, title, id: computeCaseId(file, describe, title) }
}

const FILE = 'pi/close-an-agent.spec.ts'

function baseCase() {
  return caseOf(FILE, 'can close Pi agent tab')
}

describe('auditPreservation', () => {
  it('accepts a retained case that sits where it always sat', () => {
    const spec = baseCase()
    const records = [{ originalFile: `frontend/tests/e2e/${FILE}`, originalTitle: spec.title, disposition: 'retained' }]
    expect(auditPreservation(records, discoveryOf([spec]))).toEqual([])
  })

  it('accepts a moved case whose destination the discovery holds', () => {
    const original = baseCase()
    const destination = caseOf('035-tabbar-improvements.spec.ts', original.title)
    const records = [{
      originalFile: `frontend/tests/e2e/${FILE}`,
      originalTitle: original.title,
      disposition: 'moved',
      destinationFile: 'frontend/tests/e2e/035-tabbar-improvements.spec.ts',
      destinationTitle: destination.title,
    }]
    // The original set covers the moved original even though the final tree holds only the destination.
    expect(auditPreservation(records, discoveryOf([destination]), discoveryOf([original]))).toEqual([])
  })

  it('refuses a moved case whose destination is absent', () => {
    const original = baseCase()
    const records = [{
      originalFile: `frontend/tests/e2e/${FILE}`,
      originalTitle: original.title,
      disposition: 'moved',
      destinationFile: 'frontend/tests/e2e/035-tabbar-improvements.spec.ts',
      destinationTitle: 'a title no case holds',
    }]
    const failures = auditPreservation(records, discoveryOf([]), discoveryOf([original]))
    expect(failures.some(failure => failure.includes('no destination'))).toBe(true)
  })

  it('refuses a merged case that states no required assertion', () => {
    const spec = baseCase()
    const records = [{
      originalFile: `frontend/tests/e2e/${FILE}`,
      originalTitle: spec.title,
      disposition: 'merged',
      destinationFile: `frontend/tests/e2e/${FILE}`,
      destinationTitle: 'closes the native agent and its actual owned process tree',
    }]
    const failures = auditPreservation(records, discoveryOf([spec]))
    expect(failures.some(failure => failure.includes('no required assertion'))).toBe(true)
  })

  it('accepts a merged case with its destination and required assertions', () => {
    const destination = caseOf(FILE, 'closes the native agent and its actual owned process tree')
    const records = [{
      originalFile: `frontend/tests/e2e/${FILE}`,
      originalTitle: 'can close Pi agent tab',
      disposition: 'merged',
      destinationFile: `frontend/tests/e2e/${FILE}`,
      destinationTitle: destination.title,
      requiredAssertions: ['the tab closes', 'the Worker confirms close'],
    }]
    // The original set is the pre-merge discovery: it held the original case,
    // and the destination case is new, so an explicit original manifest keeps
    // the uncovered check honest.
    expect(auditPreservation(records, discoveryOf([destination]), discoveryOf([]))).toEqual([])
  })

  it('refuses a duplicate original case', () => {
    const spec = baseCase()
    const record = { originalFile: `frontend/tests/e2e/${FILE}`, originalTitle: spec.title, disposition: 'retained' }
    const failures = auditPreservation([record, { ...record }], discoveryOf([spec]))
    expect(failures.some(failure => failure.startsWith('duplicate original case'))).toBe(true)
  })

  it('refuses an uncovered original case', () => {
    const spec = baseCase()
    const failures = auditPreservation([], discoveryOf([spec]))
    expect(failures).toEqual([`original case ${spec.id} has no preservation record`])
  })

  it('refuses a retained case that names a different destination', () => {
    const spec = baseCase()
    const records = [{
      originalFile: `frontend/tests/e2e/${FILE}`,
      originalTitle: spec.title,
      disposition: 'retained',
      destinationTitle: 'a different title',
    }]
    const failures = auditPreservation(records, discoveryOf([spec]))
    expect(failures.some(failure => failure.includes('which is a move'))).toBe(true)
  })

  it('refuses an unknown disposition', () => {
    const spec = baseCase()
    const records = [{ originalFile: `frontend/tests/e2e/${FILE}`, originalTitle: spec.title, disposition: 'deleted' }]
    const failures = auditPreservation(records, discoveryOf([spec]))
    expect(failures.some(failure => failure.includes('not retained, moved, or merged'))).toBe(true)
  })

  it('recomputes an original id through the describe path a move table stated', () => {
    const spec = caseOf('044-agent-settings.spec.ts', 'focus returns to editor after mode change', ['Agent Settings'])
    const records = [{
      originalFile: 'frontend/tests/e2e/claude-code/mode.spec.ts',
      originalTitle: 'focus returns to editor after mode change',
      disposition: 'moved',
      destinationFile: 'frontend/tests/e2e/044-agent-settings.spec.ts',
      destinationDescribe: ['Agent Settings'],
      destinationTitle: spec.title,
    }]
    expect(auditPreservation(records, discoveryOf([spec]), discoveryOf([]))).toEqual([])
  })
})

describe('computeCaseId', () => {
  it('matches the runner file hash and test-id expression with a describe', () => {
    // The id of a real discovered case: file sha1 prefix, then the
    // [project]file + U+001E-joined title path sha1 prefix.
    const id = computeCaseId('000-shared-browser.spec.ts', ['shared browser lifecycle'], 'leaves browser state for the cleanup boundary')
    expect(id).toBe('c04e2c6c29ad448da78d-f6b28868fad41f77d2fb')
  })

  it('keeps a supplied project id', () => {
    const a = computeCaseId('a.spec.ts', [], 't')
    const b = computeCaseId('a.spec.ts', [], 't', 'other-project')
    expect(a).not.toBe(b)
  })
})

describe('the repository records', () => {
  it('holds a schema-valid shape the audit accepts against a fresh full discovery', async () => {
    const { readJSON } = await import('./agent-preservation-lib.mjs')
    const root = join(dirname(fileURLToPath(import.meta.url)), '..')
    const records = readJSON(join(root, 'testdata/agent-case-preservation.json'), 'preservation records')
    expect(records.schema_version).toBe(1)
    expect(records.records.length).toBeGreaterThan(0)
    for (const record of records.records) {
      expect(['retained', 'moved', 'merged']).toContain(record.disposition)
      expect(record.originalFile).toMatch(/^frontend\/tests\/e2e\/.+\.spec\.ts$/)
      if (record.disposition === 'merged')
        expect(record.requiredAssertions.length).toBeGreaterThan(0)
    }
  })
})
