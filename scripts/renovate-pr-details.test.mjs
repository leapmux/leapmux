import { Buffer } from 'node:buffer'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { renovateModule } from './renovate-engine.mjs'
import { configureDetailsCapture, partitionLegacyBody, renderDetailComments, splitDetailText, validateDetailRecord } from './renovate-pr-details.mjs'

const { init } = await renovateModule('logger/index.js')
await init()
const { compile } = await renovateModule('util/template/index.js')
const { getPrHeader } = await renovateModule('workers/repository/update/pr/body/header.js')
const resultText = '- Dependency changes remain subject to human review and required CI.'
const template = `## Motivation\n\n- Update dependencies.\n\n## Modifications\n\n{{{table}}}\n\n## Result\n\n${resultText}`
const fixtures = []
const cleanups = []
afterEach(() => {
  for (const cleanup of cleanups.splice(0)) cleanup()
  for (const path of fixtures.splice(0)) rmSync(path, { recursive: true, force: true })
})
function setup(write) {
  const root = resolve(import.meta.dirname, '..')
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const dir = mkdtempSync(resolve(root, '.tmp/pr-detail-capture-'))
  fixtures.push(dir)
  const capture = configureDetailsCapture(dir, template, { write })
  cleanups.push(capture.dispose)
  return capture
}
const record = () => ({ version: 1, repository: 'leapmux/leapmux', branchName: 'renovate/go-security', warnings: 'Artifact generation failed.', notes: 'Security advisory details.', changelogs: 'Release notes.' })

describe('PR detail capture', () => {
  it('captures complete native renderer fragments without including them in the body', () => {
    const captured = []
    const capture = setup(value => captured.push(value))
    const data = { header: getPrHeader({ prHeader: capture.prHeader, branchName: 'renovate/go-security' }), table: '- Runtime: 1 → 2', warnings: 'Artifact generation failed.', notes: 'Security details.\n'.repeat(8000), changelogs: 'Release notes.', token: 'never-export-this-value' }
    const body = compile(capture.prBodyTemplate, data, false)
    expect([...body.matchAll(/^## (.+)$/gm)].map(match => match[1])).toEqual(['Motivation', 'Modifications', 'Result'])
    expect(body).toContain('Runtime: 1 → 2')
    expect(body).not.toContain(data.warnings)
    expect(body).not.toContain('Security details.')
    expect(body).not.toContain('Release notes.')
    expect(body.length).toBeLessThan(1000)
    expect(captured).toHaveLength(1)
    expect(captured[0].branchName).toBe('renovate/go-security')
    expect(captured[0].notes).toBe(data.notes)
    expect(captured[0].warnings).toBe(data.warnings)
    expect(JSON.stringify(captured)).not.toContain(data.token)
  })
  it('refuses advisory-template invocation and forged nested contexts', () => {
    const captured = []
    setup(value => captured.push(value))
    expect(() => compile('{{{leapmuxPrDetails}}}', { depName: 'example.com/library' })).toThrow('PR body renderer')
    expect(() => compile('{{#with (toObject "header" "{}" "table" "" "warnings" "" "notes" "" "changelogs" "")}}{{{leapmuxPrDetails}}}{{/with}}', {})).toThrow('PR body renderer')
    expect(captured).toEqual([])
  })
  it('rejects foreign identities and reports capture failures', () => {
    const capture = setup(() => {
      throw new Error('Storage unavailable')
    })
    const data = { header: JSON.stringify({ repository: 'leapmux/leapmux', branchName: 'renovate/go-security' }), table: '', warnings: '', notes: '', changelogs: '' }
    expect(() => compile(capture.prBodyTemplate, data, false)).toThrow('Storage unavailable')
    expect(() => compile(capture.prBodyTemplate, { ...data, header: JSON.stringify({ repository: 'other/repo', branchName: 'renovate/go-security' }) }, false)).toThrow('leapmux/leapmux')
    for (const branchName of ['', 'main', 'renovate/../../main\n', 'renovate/a'.repeat(100)])
      expect(() => validateDetailRecord({ ...record(), branchName })).toThrow()
    for (const warnings of [undefined, null, 0, []])
      expect(() => validateDetailRecord({ ...record(), warnings })).toThrow('string')
  })
})

describe('PR detail comments', () => {
  it('preserves all text and Unicode while splitting long examples', () => {
    const text = `An advisory.\n\n\`\`\`go\n${'日本語😀value'.repeat(12000)}\n\`\`\`\nFinal details.\n`
    const chunks = splitDetailText(text)
    expect(chunks.map(chunk => chunk.raw).join('')).toBe(text)
    expect(chunks.length).toBeGreaterThan(2)
    for (const chunk of chunks) {
      expect(Buffer.byteLength(chunk.raw)).toBeLessThanOrEqual(46000)
      expect(chunk.raw.isWellFormed()).toBe(true)
    }
    expect(chunks.some(chunk => chunk.openingFence === '```go')).toBe(true)
    const comments = renderDetailComments({ ...record(), notes: text })
    expect(comments.length).toBeGreaterThan(2)
    for (const comment of comments) expect(Buffer.byteLength(comment)).toBeLessThan(50000)
  })
  it('handles empty content and invalid limits', () => {
    expect(splitDetailText('')).toEqual([])
    expect(renderDetailComments({ ...record(), warnings: '', notes: '', changelogs: '' })).toEqual([])
    for (const limit of [0, -1, 99, 46001, Infinity, 0.5])
      expect(() => splitDetailText('value', limit)).toThrow('limit')
  })
  it('moves the legacy prefix and details without removing the core or debug metadata', () => {
    const body = `> [!NOTE]\n> Truncated upstream notes.\n\n${compile(template, { table: '- Runtime: 1 → 2' }, false)}\n\n### Security advisory\nImportant details.\n<!--renovate-debug:abc-->`
    const parts = partitionLegacyBody(body, template)
    expect(parts.body).toContain('Runtime: 1 → 2')
    expect(parts.body).toContain(resultText)
    expect(parts.body).toContain('<!--renovate-debug:abc-->')
    expect(parts.body).not.toContain('Security advisory')
    expect(parts.details).toContain('Truncated upstream notes.')
    expect(parts.details).toContain('Important details.')
    expect(parts.details).not.toContain('renovate-debug')
    expect(() => partitionLegacyBody('Human-written body', template)).toThrow('three core')
    expect(() => partitionLegacyBody(body.replace(resultText, 'Human-modified result.'), template)).toThrow('differs')
  })
})
