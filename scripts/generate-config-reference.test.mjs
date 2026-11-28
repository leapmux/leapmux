import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'
import {
  generateConfigReference,
  normalizeColumnKey,
  parseConfigReferenceMarkdown,
  parseMarkdownTableRow,
  slugifyHeading,
  stripInlineMarkdown,
} from './generate-config-reference.mjs'

const ROOT = resolve(import.meta.dirname, '..')
const REAL_CONFIG_REF_PATH = resolve(ROOT, 'site/content/docs/admin/configuration-reference.md')

describe('stripInlineMarkdown', () => {
  it('removes links, bold, and italic markers outside code spans', () => {
    expect(stripInlineMarkdown('See [Cryptography & Key Architecture](/docs/admin/cryptography/) for **details** and *(required)* values.'))
      .toBe('See Cryptography & Key Architecture for details and (required) values.')
  })

  it('preserves asterisks, underscores, and brackets inside inline code spans', () => {
    expect(stripInlineMarkdown('`captcha.*`, `__Host-`, `{"addresses": [...]}`, `storage.<name>.*`'))
      .toBe('captcha.*, __Host-, {"addresses": [...]}, storage.<name>.*')
  })

  it('collapses repeated whitespace and handles empty strings', () => {
    expect(stripInlineMarkdown('  `10`  /  `300`  ')).toBe('10 / 300')
    expect(stripInlineMarkdown('')).toBe('')
  })
})

describe('normalizeColumnKey', () => {
  it('converts headers to snake_case and strips parenthetical hints', () => {
    expect(normalizeColumnKey('Config key')).toBe('config_key')
    expect(normalizeColumnKey('Config key (`storage.<name>.*`)')).toBe('config_key')
    expect(normalizeColumnKey('CLI flag')).toBe('cli_flag')
    expect(normalizeColumnKey('Class & validation rules')).toBe('class_and_validation_rules')
  })
})

describe('slugifyHeading', () => {
  it('generates stable kebab-case slugs', () => {
    expect(slugifyHeading('Hub configuration')).toBe('hub-configuration')
    expect(slugifyHeading('PostgreSQL, CockroachDB, and YugabyteDB')).toBe('postgresql-cockroachdb-and-yugabytedb')
    expect(slugifyHeading('SQLite (default)')).toBe('sqlite-default')
  })
})

describe('parseMarkdownTableRow', () => {
  it('splits cells while preserving escaped pipes', () => {
    expect(parseMarkdownTableRow('| `a \\| b` | plain |')).toEqual(['`a | b`', 'plain'])
  })

  it('rejects a line that does not start with a pipe', () => {
    expect(() => parseMarkdownTableRow('not a table row')).toThrow('expected Markdown table row')
  })
})

describe('parseConfigReferenceMarkdown', () => {
  it('parses the real configuration-reference.md into all sections and 21 Prometheus metrics', () => {
    const markdown = readFileSync(REAL_CONFIG_REF_PATH, 'utf8')
    const data = parseConfigReferenceMarkdown(markdown)

    expect(data.schema_version).toBe(1)
    expect(data.source).toBe('/docs/admin/configuration-reference/')
    expect(data.sections.map(section => section.id)).toEqual([
      'hub-configuration',
      'worker-configuration',
      'storage-backends',
      'prometheus-metrics',
    ])

    const hubSection = data.sections.find(section => section.id === 'hub-configuration')
    expect(hubSection.groups.map(group => group.id)).toEqual([
      'server-options',
      'keys-with-no-cli-flag',
      'instance-settings-database',
      'solo-and-dev-extras-bundled-worker',
    ])

    const serverOptions = hubSection.groups.find(group => group.id === 'server-options')
    expect(serverOptions.entries.map(entry => entry.config_key)).toEqual([
      'listen',
      'data_dir',
      'dev_frontend',
      'log_level',
    ])
    expect(serverOptions.entries[0].default).toBe('(empty = platform default)')

    const noCliFlag = hubSection.groups.find(group => group.id === 'keys-with-no-cli-flag')
    expect(noCliFlag.entries).toHaveLength(1)
    expect(noCliFlag.entries[0].config_key).toBe('encryption_key_path')
    expect(noCliFlag.entries[0].environment_variable).toBe('LEAPMUX_HUB_ENCRYPTION_KEY_PATH')

    const instanceSettings = hubSection.groups.find(group => group.id === 'instance-settings-database')
    expect(instanceSettings.entries.some(entry => entry.setting_key === 'captcha.*')).toBe(true)
    expect(instanceSettings.entries.some(entry => entry.setting_key === 'rate_limit.*')).toBe(true)
    const secureCookies = instanceSettings.entries.find(entry => entry.setting_key === 'secure_cookies')
    expect(secureCookies.class_and_validation_rules).toContain('__Host-')

    const workerSection = data.sections.find(section => section.id === 'worker-configuration')
    expect(workerSection.groups.map(group => group.id)).toEqual([
      'worker-options',
      'timeout-and-limit-options',
      'worker-sqlite-options',
    ])

    const storageSection = data.sections.find(section => section.id === 'storage-backends')
    expect(storageSection.groups.map(group => group.id)).toEqual([
      'sqlite-default',
      'postgresql-cockroachdb-and-yugabytedb',
      'mysql-and-tidb',
    ])
    const pgGroup = storageSection.groups.find(group => group.id === 'postgresql-cockroachdb-and-yugabytedb')
    expect(pgGroup.key_prefix).toBe('storage.<name>.*')

    expect(data.metrics).toHaveLength(21)
    for (const metric of data.metrics) {
      expect(metric.name.startsWith('leapmux_')).toBe(true)
      expect(['Counter', 'Gauge', 'Histogram']).toContain(metric.type)
      expect(Array.isArray(metric.labels)).toBe(true)
      expect(metric.meaning.length).toBeGreaterThan(0)
    }

    const httpRequests = data.metrics.find(metric => metric.name === 'leapmux_http_requests_total')
    expect(httpRequests).toEqual({
      name: 'leapmux_http_requests_total',
      type: 'Counter',
      labels: ['method', 'path', 'status'],
      meaning: 'Total HTTP requests served.',
      group: 'HTTP and RPC metrics',
    })
    const activeWorkers = data.metrics.find(metric => metric.name === 'leapmux_active_workers')
    expect(activeWorkers.labels).toEqual([])

    // Confirm no raw Markdown syntax remains in any entry value.
    for (const section of data.sections) {
      for (const group of section.groups) {
        for (const entry of group.entries) {
          for (const value of Object.values(entry)) {
            expect(value).not.toContain('`')
            expect(value).not.toContain('](')
            expect(value).not.toContain('**')
          }
        }
      }
    }
  })

  it('ignores pipes and headings inside fenced code blocks', () => {
    const md = [
      '## Hub configuration',
      '',
      '```markdown',
      '## Fake section',
      '| a | b |',
      '| --- | --- |',
      '| 1 | 2 |',
      '```',
      '',
      '### Server options',
      '',
      '| Config key | Default |',
      '| --- | --- |',
      '| `log_level` | `info` |',
    ].join('\n')

    const data = parseConfigReferenceMarkdown(md)
    expect(data.sections).toHaveLength(1)
    expect(data.sections[0].groups).toHaveLength(1)
    expect(data.sections[0].groups[0].entries).toEqual([
      { config_key: 'log_level', default: 'info' },
    ])
  })

  it('throws when the document contains no structured tables', () => {
    expect(() => parseConfigReferenceMarkdown('## Empty\n\nNo tables here.\n'))
      .toThrow('contains no structured tables')
  })

  it('throws when a table row has a mismatched column count', () => {
    const md = [
      '## Hub configuration',
      '',
      '### Server options',
      '',
      '| Config key | Default |',
      '| --- | --- |',
      '| `log_level` |',
    ].join('\n')

    expect(() => parseConfigReferenceMarkdown(md)).toThrow('has 1 cells, expected 2')
  })
})

describe('generateConfigReference', () => {
  it('writes identical deterministic JSON on repeated runs', () => {
    const dir = mkdtempSync(join(tmpdir(), 'leapmux-config-ref-'))
    try {
      const outA = join(dir, 'a/config-reference.json')
      const outB = join(dir, 'b/config-reference.json')
      generateConfigReference({ inputPath: REAL_CONFIG_REF_PATH, outputPath: outA })
      generateConfigReference({ inputPath: REAL_CONFIG_REF_PATH, outputPath: outB })
      const textA = readFileSync(outA, 'utf8')
      const textB = readFileSync(outB, 'utf8')
      expect(textA).toBe(textB)
      expect(textA.endsWith('\n')).toBe(true)
      const parsed = JSON.parse(textA)
      expect(parsed.schema_version).toBe(1)
      expect(parsed.metrics).toHaveLength(21)
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})
