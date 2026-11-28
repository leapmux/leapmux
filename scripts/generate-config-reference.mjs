import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import process from 'node:process'

const ROOT = resolve(import.meta.dirname, '..')
const DEFAULT_INPUT_PATH = resolve(ROOT, 'site/content/docs/admin/configuration-reference.md')
const DEFAULT_OUTPUT_PATH = resolve(ROOT, 'site/public/config-reference.json')
const SOURCE_URL = '/docs/admin/configuration-reference/'

/** Convert plain prose outside inline code spans to clean text without Markdown syntax. */
function stripProseMarkdown(text) {
  return text
    .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
    .replace(/\*\*([^*]+)\*\*/g, '$1')
    .replace(/__([^_]+)__/g, '$1')
    .replace(/\*([^*]+)\*/g, '$1')
    .replace(/\\([\\`*_{}[\]()#+\-.!|])/g, '$1')
}

/** Strip inline Markdown formatting while preserving literal text inside backtick code spans. */
export function stripInlineMarkdown(text) {
  let out = ''
  let cursor = 0
  const codePattern = /`([^`]+)`/g
  for (const match of text.matchAll(codePattern)) {
    const start = match.index ?? 0
    if (start > cursor)
      out += stripProseMarkdown(text.slice(cursor, start))
    out += match[1]
    cursor = start + match[0].length
  }
  if (cursor < text.length)
    out += stripProseMarkdown(text.slice(cursor))
  return out.replace(/\s+/g, ' ').trim()
}

/** Convert a heading title into a deterministic kebab-case identifier. */
export function slugifyHeading(title) {
  return stripInlineMarkdown(title)
    .toLowerCase()
    .replace(/&/g, ' and ')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

/** Normalize a table column header into a jq-friendly snake_case property key. */
export function normalizeColumnKey(header) {
  const cleaned = stripInlineMarkdown(header)
    .replace(/\s*\([^)]*\)\s*$/, '')
    .trim()
  return cleaned
    .toLowerCase()
    .replace(/&/g, ' and ')
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
}

/** Extract an optional parenthetical key-prefix hint such as `(storage.<name>.*)` from a column header. */
function extractKeyPrefix(header) {
  const cleaned = stripInlineMarkdown(header)
  const match = cleaned.match(/\(([^)]+)\)\s*$/)
  return match
    ? match[1].trim()
    : null
}

/** Split one GitHub Flavored Markdown table row into raw cell strings, honoring escaped pipes. */
export function parseMarkdownTableRow(line) {
  const trimmed = line.trim()
  if (!trimmed.startsWith('|'))
    throw new Error(`expected Markdown table row starting with "|", got: ${line}`)

  const rawCells = []
  let current = ''
  for (let i = 0; i < trimmed.length; i++) {
    const ch = trimmed[i]
    if (ch === '\\' && trimmed[i + 1] === '|') {
      current += '|'
      i++
      continue
    }
    if (ch === '|') {
      rawCells.push(current)
      current = ''
      continue
    }
    current += ch
  }
  rawCells.push(current)

  // Drop the empty slots outside the leading and trailing pipes.
  if (rawCells.length > 0 && rawCells[0].trim() === '')
    rawCells.shift()
  if (rawCells.length > 0 && rawCells.at(-1).trim() === '')
    rawCells.pop()

  return rawCells.map(cell => cell.trim())
}

function isSeparatorRow(line) {
  if (!line.trim().startsWith('|'))
    return false
  const cells = parseMarkdownTableRow(line)
  return cells.length > 0 && cells.every(cell => /^:?-{3,}:?$/.test(cell))
}

function parseMetricLabels(raw) {
  if (!raw || raw === '—' || raw === '-' || raw.toLowerCase() === 'none')
    return []
  return raw.split(',').map(label => label.trim()).filter(Boolean)
}

function stripFrontmatter(markdown) {
  const normalized = markdown.replace(/\r\n/g, '\n')
  const match = normalized.match(/^---\n[\s\S]*?\n---\n/)
  return match
    ? normalized.slice(match[0].length)
    : normalized
}

/**
 * Parse `site/content/docs/admin/configuration-reference.md` into a deterministic,
 * jq-queryable JSON object containing all H2/H3 reference tables and Prometheus metrics.
 */
export function parseConfigReferenceMarkdown(markdown) {
  const body = stripFrontmatter(markdown)
  const lines = body.split('\n')

  const sections = []
  let currentSection = null
  let currentGroupTitle = null
  let fence = null

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    const fenceMatch = line.match(/^\s{0,3}(`{3,}|~{3,})/)
    if (fenceMatch) {
      const marker = fenceMatch[1]
      if (!fence) {
        fence = { char: marker[0], length: marker.length }
      }
      else if (marker[0] === fence.char && marker.length >= fence.length) {
        fence = null
      }
      continue
    }
    if (fence)
      continue

    const h2 = line.match(/^##\s+(\S.*)$/)
    if (h2) {
      const title = stripInlineMarkdown(h2[1])
      currentSection = {
        id: slugifyHeading(title),
        title,
        groups: [],
      }
      sections.push(currentSection)
      currentGroupTitle = null
      continue
    }

    const h3 = line.match(/^###\s+(\S.*)$/)
    if (h3) {
      currentGroupTitle = stripInlineMarkdown(h3[1])
      continue
    }

    if (line.trim().startsWith('|') && i + 1 < lines.length && isSeparatorRow(lines[i + 1])) {
      if (!currentSection)
        throw new Error(`configuration reference table at line ${i + 1} appears before any H2 section`)

      const rawHeaders = parseMarkdownTableRow(line)
      const columns = rawHeaders.map(normalizeColumnKey)
      if (new Set(columns).size !== columns.length)
        throw new Error(`duplicate normalized column names in table at line ${i + 1}: ${columns.join(', ')}`)

      const keyPrefix = rawHeaders.length > 0
        ? extractKeyPrefix(rawHeaders[0])
        : null
      const entries = []

      i += 2
      while (i < lines.length && lines[i].trim().startsWith('|')) {
        const rawCells = parseMarkdownTableRow(lines[i])
        if (rawCells.length !== columns.length) {
          throw new Error(
            `table row at line ${i + 1} has ${rawCells.length} cells, expected ${columns.length}`,
          )
        }
        const entry = {}
        for (let colIndex = 0; colIndex < columns.length; colIndex++)
          entry[columns[colIndex]] = stripInlineMarkdown(rawCells[colIndex])
        entries.push(entry)
        i++
      }
      // Step back one line because the outer for-loop increments i.
      i--

      const groupTitle = currentGroupTitle ?? currentSection.title
      const group = {
        id: slugifyHeading(groupTitle),
        title: groupTitle,
        columns,
        entries,
      }
      if (keyPrefix)
        group.key_prefix = keyPrefix
      currentSection.groups.push(group)
    }
  }

  const populatedSections = sections.filter(section => section.groups.length > 0)
  if (populatedSections.length === 0)
    throw new Error('configuration reference contains no structured tables')

  const metricsSection = populatedSections.find(section => section.id === 'prometheus-metrics')
  const metrics = []
  if (metricsSection) {
    for (const group of metricsSection.groups) {
      for (const entry of group.entries) {
        if (!entry.metric)
          continue
        metrics.push({
          name: entry.metric,
          type: entry.type ?? '',
          labels: parseMetricLabels(entry.labels ?? ''),
          meaning: entry.meaning ?? '',
          group: group.title,
        })
      }
    }
  }

  return {
    schema_version: 1,
    source: SOURCE_URL,
    sections: populatedSections,
    metrics,
  }
}

/** Generate `config-reference.json` deterministically from the configuration reference Markdown. */
export function generateConfigReference({
  inputPath = DEFAULT_INPUT_PATH,
  outputPath = DEFAULT_OUTPUT_PATH,
} = {}) {
  const markdown = readFileSync(inputPath, 'utf8')
  const data = parseConfigReferenceMarkdown(markdown)
  mkdirSync(dirname(outputPath), { recursive: true })
  writeFileSync(outputPath, `${JSON.stringify(data, null, 2)}\n`, 'utf8')
  return data
}

function parseCliArgs(args) {
  let inputPath = DEFAULT_INPUT_PATH
  let outputPath = DEFAULT_OUTPUT_PATH
  for (let i = 0; i < args.length; i++) {
    const arg = args[i]
    if (arg === '--input' && args[i + 1]) {
      inputPath = resolve(args[++i])
    }
    else if (arg === '--output' && args[i + 1]) {
      outputPath = resolve(args[++i])
    }
    else if (!arg.startsWith('-')) {
      outputPath = resolve(arg)
    }
    else {
      throw new Error(`unknown argument: ${arg}`)
    }
  }
  return { inputPath, outputPath }
}

if (import.meta.main) {
  const options = parseCliArgs(process.argv.slice(2))
  const data = generateConfigReference(options)
  const totalEntries = data.sections.reduce(
    (sum, section) => sum + section.groups.reduce((gSum, group) => gSum + group.entries.length, 0),
    0,
  )
  console.log(
    `Generated ${options.outputPath} (${data.sections.length} sections, ${totalEntries} entries, ${data.metrics.length} metrics).`,
  )
}
