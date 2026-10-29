import { Buffer } from 'node:buffer'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { renovateModule } from './renovate-engine.mjs'
import { configureDetailsCapture, partitionLegacyBody, renderDetailComments, splitDetailText, validateDetailRecord } from './renovate-pr-details.mjs'

const { init } = await renovateModule('logger/index.js')
await init()
const { compile } = await renovateModule('util/template/index.js')
const { getPrHeader } = await renovateModule('workers/repository/update/pr/body/header.js')
const { getPrBody } = await renovateModule('workers/repository/update/pr/body/index.js')
const { setPlatformApi } = await renovateModule('modules/platform/index.js')
const requireRenovate = createRequire(new URL('../.github/renovate/package.json', import.meta.url))
const MarkdownIt = requireRenovate('markdown-it')
const { parse: parseHtml } = requireRenovate('node-html-parser')
const markdown = new MarkdownIt({ html: true })
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
function record() {
  return {
    version: 1,
    repository: 'leapmux/leapmux',
    branchName: 'renovate/go-security',
    warnings: 'Artifact generation failed.',
    notes: 'Security advisory details.',
    changelogs: 'Release notes.',
  }
}

describe('PR detail capture', () => {
  it('runs through the pinned PR body renderer before platform truncation', () => {
    const captured = []
    const capture = setup(value => captured.push(value))
    setPlatformApi('github')
    const notes = 'Complete advisory details.\n'.repeat(5000)
    const branch = {
      ...capture,
      branchName: 'renovate/go-security',
      recreateClosed: false,
      prBodyColumns: ['Package', 'Change'],
      warnings: [
        { topic: 'fixture', message: 'A warning for the comment.' },
      ],
      upgrades: [
        {
          depName: 'example.com/library',
          currentValue: '1.0.0',
          newValue: '1.1.0',
          prBodyDefinitions: { Package: '{{depName}}', Change: '{{currentValue}} → {{newValue}}' },
          prBodyNotes: [notes],
        },
      ],
    }
    const body = getPrBody(branch, { debugData: {} }, branch)
    expect(body).toContain('example.com/library')
    expect(body).not.toContain('Complete advisory details.')
    expect(body).not.toContain('truncated')
    expect(captured).toHaveLength(1)
    expect(captured[0].notes).toContain(notes.trim())
    expect(captured[0].notes.length).toBeGreaterThan(58000)
    expect(captured[0].warnings).toContain('A warning for the comment.')
  })
  it('captures complete native renderer fragments without including them in the body', () => {
    const captured = []
    const capture = setup(value => captured.push(value))
    const data = {
      header: getPrHeader({ prHeader: capture.prHeader, branchName: 'renovate/go-security' }),
      table: '- Runtime: 1 → 2',
      warnings: 'Artifact generation failed.',
      notes: 'Security details.\n'.repeat(8000),
      changelogs: 'Release notes.',
      token: 'never-export-this-value',
    }
    const body = compile(capture.prBodyTemplate, data, false)
    expect([
      ...body.matchAll(/^## (.+)$/gm),
    ].map(match => match[1])).toEqual(['Motivation', 'Modifications', 'Result'])
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
    const data = {
      header: JSON.stringify({ repository: 'leapmux/leapmux', branchName: 'renovate/go-security' }),
      table: '',
      warnings: '',
      notes: '',
      changelogs: '',
    }
    expect(() => compile(capture.prBodyTemplate, data, false)).toThrow('Storage unavailable')
    expect(() => compile(capture.prBodyTemplate, {
      ...data,
      header: JSON.stringify({ repository: 'other/repo', branchName: 'renovate/go-security' }),
    }, false)).toThrow('leapmux/leapmux')
    for (const branchName of [
      '',
      'main',
      'renovate/../../main\n',
      'renovate/a'.repeat(100),
    ]) {
      expect(() => validateDetailRecord({
        ...record(),
        branchName,
      })).toThrow()
    }
    for (const warnings of [undefined, null, 0, []]) {
      expect(() => validateDetailRecord({
        ...record(),
        warnings,
      })).toThrow('string')
    }
  })
})

describe('PR detail comments', () => {
  function expectCollapsed(text, limit = 46000) {
    const chunks = splitDetailText(text, limit)
    expect(chunks.map(chunk => chunk.raw).join('')).toBe(text)
    for (const chunk of chunks) {
      const body = `${chunk.prefix}${chunk.raw}${chunk.suffix}`
      expect(Buffer.byteLength(body)).toBeLessThanOrEqual(limit)
      expect(body.isWellFormed()).toBe(true)
      const html = markdown.render(body)
      expect((html.match(/<details\b/gi) ?? []).length).toBe((html.match(/<\/details>/gi) ?? []).length)
      const document = parseHtml(html)
      for (const details of document.querySelectorAll('details')) {
        expect(details.hasAttribute('open')).toBe(false)
        expect(details.querySelector('summary')?.text.trim()).toBeTruthy()
        details.remove()
      }
      expect(document.text).not.toContain('secret')
    }
    return chunks
  }
  it('keeps a complete collapsed section together when it fits in a fresh comment', () => {
    const section = `<details>\n<summary>More information</summary>\n\n${'secret\n'.repeat(20)}\n</details>`
    const chunks = expectCollapsed(`${'Introduction.\n'.repeat(15)}\n${section}`, 300)
    expect(chunks).toHaveLength(2)
    expect(chunks[1].raw).toBe(section)
  })
  it('reopens an oversized collapsed section with its summary in every comment', () => {
    const text = `<details>\n<summary>More information</summary>\n\n${'secret 日本語😀'.repeat(10000)}\n\n</details>`
    const chunks = expectCollapsed(text)
    expect(chunks.length).toBeGreaterThan(3)
    for (const chunk of chunks)
      expect(`${chunk.prefix}${chunk.raw}${chunk.suffix}`).toContain('<summary>More information</summary>')
    const comments = renderDetailComments({
      ...record(),
      warnings: '',
      notes: text,
      changelogs: '',
    })
    for (const comment of comments) {
      expect(Buffer.byteLength(comment)).toBeLessThan(50000)
      const document = parseHtml(markdown.render(comment))
      expect(document.querySelectorAll('details')).toHaveLength(1)
      document.querySelector('details').remove()
      expect(document.text).not.toContain('secret')
    }
  })
  it('preserves nested sections and code fences across comment boundaries', () => {
    const text = `<details>\n<summary>Outer information</summary>\n\n<details>\n<summary>Inner information</summary>\n\n\`\`\`html\n${'secret </details> <details> <!--\n'.repeat(120)}\`\`\`\n\n</details>\n</details>`
    const chunks = expectCollapsed(text, 400)
    expect(chunks.length).toBeGreaterThan(3)
    const continuation = parseHtml(markdown.render(`${chunks[1].prefix}${chunks[1].raw}${chunks[1].suffix}`))
    expect(continuation.querySelector('details > summary').text).toBe('Outer information')
    expect(continuation.querySelector('details details > summary').text).toBe('Inner information')
    expect(continuation.querySelector('details details pre')?.text).toContain('secret')
  })
  it('handles multiline summaries, quoted attributes, and CRLF input', () => {
    const text = `<DETAILS title="a > b">\r\n<SUMMARY>More\r\n<strong>information</strong></SUMMARY>\r\n\r\n${'secret\r\n'.repeat(150)}</DETAILS>`
    const chunks = expectCollapsed(text, 300)
    expect(chunks.length).toBeGreaterThan(2)
    expect(chunks[1].prefix).toContain('<SUMMARY>More\r\n<strong>information</strong></SUMMARY>')
  })
  it('preserves a same-line summary and a fenced example with CRLF endings', () => {
    const text = `<details><summary>More information</summary>\r\n\r\n~~~html\r\n${'secret <details>\r\n'.repeat(100)}~~~\r\n\r\n</details>`
    expect(expectCollapsed(text, 300).length).toBeGreaterThan(2)
  })
  it('ignores details examples in inline code, indented code, and HTML comments', () => {
    const examples = '`<details>`\n\n    <details>\n\n<!-- <details><summary>example</summary> -->\n\n'
    const chunks = splitDetailText(`${examples}${'ordinary text\n'.repeat(50)}`, 300)
    expect(chunks.map(chunk => chunk.raw).join('')).toBe(`${examples}${'ordinary text\n'.repeat(50)}`)
    expect(chunks.every(chunk => !chunk.prefix && !chunk.suffix)).toBe(true)
  })
  it('ignores details examples inside another HTML tag attribute', () => {
    const text = '<div title="<details>">Ordinary text.</div>'
    const chunks = splitDetailText(text)
    expect(chunks).toEqual([
      { raw: text, prefix: '', suffix: '' },
    ])
  })
  it('closes truncated sections and supplies a continuation summary when none exists', () => {
    const text = `<details>\n\n${'secret\n'.repeat(150)}`
    const chunks = splitDetailText(text, 300)
    expect(chunks.map(chunk => chunk.raw).join('')).toBe(text)
    for (const chunk of chunks) {
      const body = `${chunk.prefix}${chunk.raw}${chunk.suffix}`
      expect(body.trimEnd().endsWith('</details>')).toBe(true)
      expect(Buffer.byteLength(body)).toBeLessThanOrEqual(300)
    }
    expect(chunks[1].prefix).toContain('<summary>More information</summary>')
  })
  it('closes an unfinished code fence before closing its truncated section', () => {
    const text = `<details>\n<summary>More information</summary>\n\n\`\`\`go\n${'secret\n'.repeat(100)}`
    const chunks = expectCollapsed(text, 300)
    expect(chunks.at(-1).suffix).toBe('\n```\n\n</details>')
  })
  it('counts container markup at exact byte boundaries and preserves empty sections', () => {
    for (const size of [0, 1, 21, 22, 100, 1000]) {
      const text = `<details><summary>More information</summary>\n\n${'secret 😀'.repeat(size)}\n</details>`
      for (const limit of [100, 101, 128, 256]) expectCollapsed(text, limit)
    }
  })
  it('fails visibly if a section header cannot fit without losing its text', () => {
    const text = `<details><summary>${'A'.repeat(400)}</summary>\n\nsecret\n</details>`
    expect(() => splitDetailText(text, 300)).toThrow('markup')
  })
  it('rejects malformed section markup without publishing exposed content', () => {
    expect(() => splitDetailText('</details>')).toThrow('unmatched closing')
    expect(() => splitDetailText('<details><summary>Missing end')).toThrow('incomplete summary')
  })
  it('preserves all text and Unicode while splitting long examples', () => {
    const text = `An advisory.\n\n\`\`\`go\n${'日本語😀value'.repeat(12000)}\n\`\`\`\nFinal details.\n`
    const chunks = splitDetailText(text)
    expect(chunks.map(chunk => chunk.raw).join('')).toBe(text)
    expect(chunks.length).toBeGreaterThan(2)
    for (const chunk of chunks) {
      expect(Buffer.byteLength(chunk.raw)).toBeLessThanOrEqual(46000)
      expect(chunk.raw.isWellFormed()).toBe(true)
    }
    expect(chunks.some(chunk => chunk.prefix.includes('```go'))).toBe(true)
    const comments = renderDetailComments({
      ...record(),
      notes: text,
    })
    expect(comments.length).toBeGreaterThan(2)
    for (const comment of comments) expect(Buffer.byteLength(comment)).toBeLessThan(50000)
  })
  it('handles empty content and invalid limits', () => {
    expect(splitDetailText('')).toEqual([])
    expect(renderDetailComments({
      ...record(),
      warnings: '',
      notes: '',
      changelogs: '',
    })).toEqual([])
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
