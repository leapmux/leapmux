import { describe, expect, it } from 'bun:test'
import { resetMatrixStatus, verifyAcceptance } from './verify-agent-feature-acceptance.mjs'

/** One discovered case keyed the way a --list manifest states it. */
function discovered(file, title, id = `${file}#${title}`) {
  return { suites: [{ title: file, file, specs: [{ file, line: 1, title, id }] }] }
}

/** One passed first attempt shaped like a combined report entry. */
function passed(spec) {
  return { suites: [{ title: spec.file, file: spec.file, specs: [{ ...spec, tests: [{ expectedStatus: 'passed', results: [{ status: 'passed' }] }] }] }] }
}

function checklistWith(spec) {
  return { cells: { 'basic-chat': { 'claude-code': { support: 'supported', spec } } } }
}

describe('verifyAcceptance', () => {
  const spec = { file: 'pi/basic-chat.spec.ts', title: 'answers in its own tab', id: 'pi-basic-1' }

  it('accepts a cell whose every discovered case passes first-attempt', () => {
    expect(verifyAcceptance(checklistWith('frontend/tests/e2e/pi/basic-chat.spec.ts'), passed(spec), discovered(spec.file, spec.title, spec.id))).toEqual([])
  })

  it('refuses a case the report omits', () => {
    const failures = verifyAcceptance(checklistWith('frontend/tests/e2e/pi/basic-chat.spec.ts'), { suites: [] }, discovered(spec.file, spec.title, spec.id))
    expect(failures).toEqual([`case ${spec.title} of frontend/tests/e2e/pi/basic-chat.spec.ts (basic-chat/claude-code) has no report result`])
  })

  it('refuses a case that passed only after a retry', () => {
    const retried = { suites: [{ title: spec.file, file: spec.file, specs: [{ ...spec, tests: [{ results: [{ status: 'failed' }, { status: 'passed' }] }] }] }] }
    const failures = verifyAcceptance(checklistWith('frontend/tests/e2e/pi/basic-chat.spec.ts'), retried, discovered(spec.file, spec.title, spec.id))
    expect(failures.some(failure => failure.includes('does not pass on the first attempt'))).toBe(true)
  })

  it('refuses a skipped case even when its only attempt passed', () => {
    const skipped = { suites: [{ title: spec.file, file: spec.file, specs: [{ ...spec, tests: [{ expectedStatus: 'skipped', results: [{ status: 'passed' }] }] }] }] }
    const failures = verifyAcceptance(checklistWith('frontend/tests/e2e/pi/basic-chat.spec.ts'), skipped, discovered(spec.file, spec.title, spec.id))
    expect(failures.some(failure => failure.includes('does not pass on the first attempt'))).toBe(true)
  })

  it('refuses a cell whose spec file holds no discovered case', () => {
    const failures = verifyAcceptance(checklistWith('frontend/tests/e2e/pi/absent.spec.ts'), passed(spec), discovered(spec.file, spec.title, spec.id))
    expect(failures.some(failure => failure.includes('holds no discovered case'))).toBe(true)
  })

  it('refuses two cells that name the same spec file', () => {
    const checklist = { cells: {
      'basic-chat': { 'claude-code': { spec: 'frontend/tests/e2e/pi/basic-chat.spec.ts' }, 'pi': { spec: 'frontend/tests/e2e/pi/basic-chat.spec.ts' } },
    } }
    const failures = verifyAcceptance(checklist, passed(spec), discovered(spec.file, spec.title, spec.id))
    expect(failures.some(failure => failure.includes('name the same spec file'))).toBe(true)
  })

  it('refuses a report and discovery from different frozen sources', () => {
    const failures = verifyAcceptance(checklistWith('frontend/tests/e2e/pi/basic-chat.spec.ts'), passed(spec), { ...discovered(spec.file, spec.title, spec.id), identity: { head: 'b' } }, { head: 'a' })
    expect(failures.some(failure => failure.includes('frozen source'))).toBe(true)
  })
})

describe('resetMatrixStatus', () => {
  it('clears passed statuses and verification while keeping support and notes', () => {
    const checklist = { cells: { 'basic-chat': { 'claude-code': {
      support: 'agent-limit',
      userNote: 'a note',
      detailNote: 'a detail',
      spec: 'frontend/tests/e2e/pi/basic-chat.spec.ts',
      testStatus: 'passed',
      verified: true,
    }, 'pi': { support: 'supported', testStatus: 'pending', verified: false } } } }
    const reset = resetMatrixStatus(checklist)
    expect(reset).toBe(1)
    const cell = checklist.cells['basic-chat']['claude-code']
    expect(cell.testStatus).toBe('pending')
    expect(cell.verified).toBe(false)
    expect(cell.support).toBe('agent-limit')
    expect(cell.userNote).toBe('a note')
    expect(cell.spec).toBe('frontend/tests/e2e/pi/basic-chat.spec.ts')
    expect(checklist.cells['basic-chat'].pi.testStatus).toBe('pending')
  })
})
