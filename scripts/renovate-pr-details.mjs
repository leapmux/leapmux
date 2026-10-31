import { Buffer } from 'node:buffer'
import { appendFileSync, mkdirSync } from 'node:fs'
import { isAbsolute, resolve } from 'node:path'
import { registerRootHelper, requireRenovate } from './renovate-engine.mjs'

const contentFields = [
  'header',
  'table',
  'warnings',
  'notes',
  'changelogs',
]
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
      throw new TypeError(`PR details require a string ${field} field.`)
  }
  return record
}

/** Capture the renderer's complete fragments before GitHub truncates the PR body. */
export function configureDetailsCapture(trusted, template, { write = record => appendFileSync(resolve(trusted, '.tmp/renovate-pr-details.jsonl'), `${JSON.stringify(record)}\n`) } = {}) {
  if (!isAbsolute(trusted) || typeof template !== 'string')
    throw new Error('PR detail capture requires an absolute trusted checkout and a body template.')
  const dispose = registerRootHelper('leapmuxPrDetails', {
    // Advisory notes are also compiled as templates. They must not invoke this writer.
    accepts: root => contentFields.every(field => Object.hasOwn(root ?? {}, field) && typeof root[field] === 'string'),
    rejection: 'PR details can only be captured from the PR body renderer.',
  }, (root) => {
    const identity = JSON.parse(root.header.trim())
    const record = validateDetailRecord({
      version: 1,
      repository: identity.repository,
      branchName: identity.branchName,
      ...Object.fromEntries(DETAIL_FIELDS.map(field => [field, root[field]])),
    })
    mkdirSync(resolve(trusted, '.tmp'), { recursive: true })
    write(record)
    return ''
  })
  return {
    prHeader: '{"repository":"leapmux/leapmux","branchName":{{{toJSON branchName}}} }',
    prBodyTemplate: `${template}\n{{{leapmuxPrDetails}}}`,
    dispose,
  }
}

function findDetailMarkup(text) {
  // Use Markdown source positions so examples and HTML comments cannot change the container stack.
  const { remark } = requireRenovate('remark')
  const tags = []
  const spans = []
  const visit = (node) => {
    const start = node.position.start.offset
    const end = node.position.end.offset
    if (node.type === 'html') {
      const source = text.slice(start, end)
      for (const match of source.matchAll(/<!--[\s\S]*?(?:-->|$)|<\/?([a-z][\w-]*)\b(?:[^"'<>]|"[^"]*"|'[^']*')*>/gi)) {
        if (['details', 'summary'].includes(match[1]?.toLowerCase())) {
          tags.push({
            start: start + match.index,
            end: start + match.index + match[0].length,
            kind: match[1].toLowerCase(),
            opening: !match[0].startsWith('</'),
          })
        }
      }
    }
    else if (node.type === 'code') {
      const source = text.slice(start, end)
      const marker = /^ {0,3}(`{3,}|~{3,})/.exec(source)?.[1]
      if (marker) {
        const newline = source.indexOf('\n')
        const openingEnd = newline < 0
          ? source.length
          : newline + 1
        const firstLine = source.slice(0, openingEnd).trimEnd()
        spans.push({
          start,
          end: start + openingEnd,
          kind: 'fence',
          opening: true,
          prefix: `${firstLine}\n`,
          suffix: `\n${marker}`,
        })
        const last = /(?:^|\n) {0,3}(`{3,}|~{3,})[ \t]*$/.exec(source)
        if (last && last.index >= firstLine.length && last[1][0] === marker[0] && last[1].length >= marker.length) {
          spans.push({
            start: start + last.index + (source[last.index] === '\n'
              ? 1
              : 0),
            end,
            kind: 'fence',
            opening: false,
          })
        }
      }
    }
    for (const child of node.children ?? []) visit(child)
  }
  visit(remark().parse(text))
  for (let index = 0; index < tags.length; index++) {
    const tag = tags[index]
    if (tag.kind !== 'details')
      continue
    if (tag.opening) {
      const summary = tags[index + 1]
      let end = tag.end
      if (summary?.kind === 'summary' && summary.opening && !text.slice(tag.end, summary.start).trim()) {
        const closing = tags[index + 2]
        if (closing?.kind !== 'summary' || closing.opening)
          throw new Error('PR detail markup contains an incomplete summary.')
        end = closing.end
        index += 2
      }
      const header = text.slice(tag.start, end)
      const fallbackSummary = end === tag.end
        ? '\n<summary>More information</summary>'
        : ''
      spans.push({
        ...tag,
        end,
        prefix: `${header}${fallbackSummary}\n\n`,
        suffix: '\n\n</details>',
      })
    }
    else {
      spans.push(tag)
    }
  }
  spans.sort((a, b) => a.start - b.start)
  const stack = []
  for (const [index, span] of spans.entries()) {
    if (span.opening) {
      stack.push(index)
    }
    else {
      const opening = spans[stack.pop()]
      if (opening?.kind !== span.kind)
        throw new Error('PR detail markup contains an unmatched closing tag.')
      opening.closingIndex = index
    }
  }
  return spans
}

/** Split losslessly, and close and reopen Markdown containers within each comment's byte limit. */
export function splitDetailText(text, limit = 46000) {
  if (typeof text !== 'string' || !Number.isSafeInteger(limit) || limit < 100 || limit > 46000)
    throw new Error('Comment chunks require text and a byte limit from 100 through 46000.')
  const chunks = []
  const spans = findDetailMarkup(text)
  let containers = []
  let raw = ''
  let rawBytes = 0
  let prefix = ''
  const opening = () => containers.map(container => container.prefix).join('')
  const closing = (values = containers) => values.toReversed().map(container => container.suffix).join('')
  const flush = () => {
    if (!raw)
      return
    chunks.push({
      raw,
      prefix,
      suffix: closing(),
    })
    raw = ''
    rawBytes = 0
    prefix = opening()
  }
  const fits = (value, next = containers) => Buffer.byteLength(prefix) + rawBytes + Buffer.byteLength(value) + Buffer.byteLength(closing(next)) <= limit
  const appendMarkup = (value, next) => {
    if (!fits(value, next))
      flush()
    if (!fits(value, next))
      throw new Error('PR detail markup cannot fit in one comment without losing text.')
    raw += value
    rawBytes += Buffer.byteLength(value)
    containers = next
  }
  const appendText = (value) => {
    for (const line of value.match(/[^\n]*\n|[^\n]+$/g) ?? []) {
      if (raw && !fits(line))
        flush()
      for (const character of line) {
        if (!fits(character))
          flush()
        if (!fits(character))
          throw new Error('PR detail markup leaves no room for the next text character.')
        raw += character
        rawBytes += Buffer.byteLength(character)
      }
    }
  }
  let position = 0
  for (let index = 0; index < spans.length; index++) {
    const span = spans[index]
    appendText(text.slice(position, span.start))
    if (span.opening && span.closingIndex !== undefined) {
      const end = spans[span.closingIndex].end
      const complete = text.slice(span.start, end)
      // Keep a complete section or example intact when it fits in a fresh comment.
      if (fits(complete) || Buffer.byteLength(opening()) + Buffer.byteLength(complete) + Buffer.byteLength(closing()) <= limit) {
        appendMarkup(complete, containers)
        position = end
        index = span.closingIndex
        continue
      }
    }
    appendMarkup(text.slice(span.start, span.end), span.opening
      ? [...containers, span]
      : containers.slice(0, -1))
    position = span.end
  }
  appendText(text.slice(position))
  flush()
  return chunks
}

export function renderDetailComments(record) {
  validateDetailRecord(record)
  const text = DETAIL_FIELDS.map(field => record[field].trim()).filter(Boolean).join('\n\n---\n\n')
  return splitDetailText(text).map((chunk, index, chunks) => {
    const body = `${COMMENT_MARKER}${index + 1} -->\n## Renovate update details (${index + 1}/${chunks.length})\n\n${chunk.prefix}${chunk.raw}${chunk.suffix}`
    if (Buffer.byteLength(body) > 50000)
      throw new Error('A PR detail comment exceeds the safe GitHub comment limit.')
    return body
  })
}

/** Migrate the existing bot description only when its fixed Result text still matches. */
export function partitionLegacyBody(body, template) {
  const headings = [
    ...body.matchAll(/^## (Motivation|Modifications|Result)\s*$/gm),
  ]
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
  return {
    body: `${core}${debug.length
      ? `\n${debug.join('\n')}`
      : ''}\n`,
    details: [prefix, details].filter(Boolean).join('\n\n'),
  }
}
