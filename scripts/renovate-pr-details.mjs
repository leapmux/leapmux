import { Buffer } from 'node:buffer'
import { appendFileSync, mkdirSync } from 'node:fs'
import { createRequire } from 'node:module'
import { isAbsolute, resolve } from 'node:path'

const requireRenovate = createRequire(new URL('../.github/renovate/package.json', import.meta.url))
const requireRenderer = createRequire(requireRenovate.resolve('renovate/dist/util/template/index.js'))
const contentFields = ['header', 'table', 'warnings', 'notes', 'changelogs']
export const DETAIL_FIELDS = ['warnings', 'notes', 'changelogs']
export const COMMENT_MARKER = '<!-- leapmux-renovate-details:v1 part='

export function validateDetailRecord(record) {
  if (!record || record.version !== 1 || record.repository !== 'leapmux/leapmux'
    || typeof record.branchName !== 'string' || record.branchName.length > 240
    || !/^renovate\/[a-z0-9][a-z0-9./_-]*$/.test(record.branchName) || record.branchName.includes('..')
    || record.branchName.includes('//') || /[/.]$/.test(record.branchName)) {
    throw new Error('PR details require a Renovate branch in leapmux/leapmux.')
  }
  for (const field of DETAIL_FIELDS) {
    if (typeof record[field] !== 'string')
      throw new Error(`PR details require a string ${field} field.`)
  }
  return record
}

/** Capture the renderer's complete fragments before GitHub truncates the PR body. */
export function configureDetailsCapture(trusted, template, { write = record => appendFileSync(resolve(trusted, '.tmp/renovate-pr-details.jsonl'), `${JSON.stringify(record)}\n`) } = {}) {
  if (!isAbsolute(trusted) || typeof template !== 'string')
    throw new Error('PR detail capture requires an absolute trusted checkout and a body template.')
  const handlebars = requireRenderer('handlebars')
  const previous = handlebars.helpers.leapmuxPrDetails
  handlebars.registerHelper('leapmuxPrDetails', function (options) {
    const root = options.data?.root
    // Advisory notes are also compiled as templates. They must not invoke this writer.
    if (this !== root || !contentFields.every(field => Object.hasOwn(root ?? {}, field) && typeof root[field] === 'string'))
      throw new Error('PR details can only be captured from the PR body renderer.')
    const identity = JSON.parse(root.header.trim())
    const record = validateDetailRecord({ version: 1, repository: identity.repository, branchName: identity.branchName, ...Object.fromEntries(DETAIL_FIELDS.map(field => [field, root[field]])) })
    mkdirSync(resolve(trusted, '.tmp'), { recursive: true })
    write(record)
    return ''
  })
  return {
    prHeader: '{"repository":"leapmux/leapmux","branchName":{{{toJSON branchName}}} }',
    prBodyTemplate: `${template}\n{{{leapmuxPrDetails}}}`,
    dispose() {
      if (previous)
        handlebars.registerHelper('leapmuxPrDetails', previous)
      else
        handlebars.unregisterHelper('leapmuxPrDetails')
    },
  }
}

/** Split without losing UTF-8 text, and keep fenced examples readable across comments. */
export function splitDetailText(text, limit = 46000) {
  if (typeof text !== 'string' || !Number.isSafeInteger(limit) || limit < 100 || limit > 46000)
    throw new Error('Comment chunks require text and a byte limit from 100 through 46000.')
  const chunks = []
  let raw = ''
  let fence = null
  let openingFence = null
  const flush = () => {
    if (!raw)
      return
    chunks.push({ raw, openingFence: openingFence?.line ?? '', closingFence: fence?.marker ?? '' })
    raw = ''
    openingFence = fence
  }
  for (const line of text.match(/[^\n]*\n|[^\n]+$/g) ?? []) {
    let remainder = line
    if (raw && Buffer.byteLength(raw + remainder) > limit)
      flush()
    while (Buffer.byteLength(remainder) > limit) {
      let count = 0
      let end = 0
      for (const character of remainder) {
        const size = Buffer.byteLength(character)
        if (count + size > limit)
          break
        count += size
        end += character.length
      }
      raw += remainder.slice(0, end)
      remainder = remainder.slice(end)
      flush()
    }
    raw += remainder
    const match = /^ {0,3}(`{3,}|~{3,})([^\r\n]*)/.exec(line)
    if (match) {
      if (!fence)
        fence = { marker: match[1], line: line.trimEnd() }
      else if (match[1][0] === fence.marker[0] && match[1].length >= fence.marker.length && !match[2].trim())
        fence = null
    }
  }
  flush()
  return chunks
}

export function renderDetailComments(record) {
  validateDetailRecord(record)
  const text = DETAIL_FIELDS.map(field => record[field].trim()).filter(Boolean).join('\n\n---\n\n')
  return splitDetailText(text).map((chunk, index, chunks) => {
    const before = chunk.openingFence ? `${chunk.openingFence}\n` : ''
    const after = chunk.closingFence ? `\n${chunk.closingFence}` : ''
    const body = `${COMMENT_MARKER}${index + 1} -->\n## Renovate update details (${index + 1}/${chunks.length})\n\n${before}${chunk.raw}${after}`
    if (Buffer.byteLength(body) > 50000)
      throw new Error('A PR detail comment exceeds the safe GitHub comment limit.')
    return body
  })
}

/** Migrate the existing bot description only when its fixed Result text still matches. */
export function partitionLegacyBody(body, template) {
  const headings = [...body.matchAll(/^## (Motivation|Modifications|Result)\s*$/gm)]
  if (headings.map(match => match[1]).join(',') !== 'Motivation,Modifications,Result')
    throw new Error('The existing PR must contain the three core Renovate sections.')
  const resultText = template.slice(template.indexOf('## Result') + '## Result'.length).trim()
  const resultStart = headings[2].index + headings[2][0].length
  const tail = body.slice(resultStart).trimStart()
  if (!resultText || !tail.startsWith(resultText))
    throw new Error('The existing PR Result text differs from the configured template.')
  const prefix = body.slice(0, headings[0].index).trim()
  const suffix = tail.slice(resultText.length).trim()
  const debug = suffix.match(/<!--renovate-debug:[\s\S]*?-->/g) ?? []
  const details = suffix.replace(/<!--renovate-debug:[\s\S]*?-->/g, '').trim()
  const core = `${body.slice(headings[0].index, resultStart).trimEnd()}\n\n${resultText}`
  return { body: `${core}${debug.length ? `\n${debug.join('\n')}` : ''}\n`, details: [prefix, details].filter(Boolean).join('\n\n') }
}
