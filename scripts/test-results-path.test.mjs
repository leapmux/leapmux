import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { testReportPath } from './test-results-path.mjs'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

describe('testReportPath', () => {
  it('creates an absolute report path and accepts absent configuration', () => {
    expect(testReportPath('fixture', {})).toBeUndefined()
    expect(testReportPath('fixture', { LEAPMUX_TEST_RESULTS_DIR: '' })).toBeUndefined()
    const root = scratch.create('structured report path')
    const directory = join(root, 'new reports')
    expect(testReportPath('fixture', { LEAPMUX_TEST_RESULTS_DIR: directory })).toBe(join(directory, 'fixture.xml'))
    expect(existsSync(directory)).toBe(true)
  })

  it('rejects relative directories, NUL and unsafe labels', () => {
    expect(() => testReportPath('fixture', { LEAPMUX_TEST_RESULTS_DIR: 'relative' })).toThrow('absolute')
    const root = scratch.create('unsafe-report')
    expect(() => testReportPath('fixture', { LEAPMUX_TEST_RESULTS_DIR: `${root}\0` })).toThrow('NUL')
    for (const label of ['', undefined, 0, '../escape'])
      expect(() => testReportPath(label, { LEAPMUX_TEST_RESULTS_DIR: root })).toThrow('label')
  })
})
