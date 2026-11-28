import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { join, relative, resolve } from 'node:path'
import process from 'node:process'
import { markdownBlockTexts, markdownLinks, markdownText } from './matrix-markdown.mjs'
import { readCodingAgentMatrix } from './validate-coding-agent-matrix.mjs'

const ROOT = resolve(import.meta.dirname, '..')
const require = createRequire(new URL('../frontend/package.json', import.meta.url))
const { JSDOM } = require('jsdom')

/** Compare text the way a reader sees it: one space between words, plain quotes and dashes. */
function normalizeText(value) {
  return value
    .replace(/[‘’]/g, '\'')
    .replace(/[“”]/g, '"')
    .replace(/[–—]/g, '-')
    .trim()
    .replace(/\s+/g, ' ')
}

function sameText(actual, expected) {
  return normalizeText(actual) === normalizeText(expected)
}

function renderedText(node) {
  if (node.nodeType === 3)
    return node.textContent ?? ''
  return Array.from(node.childNodes).map(renderedText).join(' ')
}

function noteMatches(node, source, removeLabel = false) {
  if (!node)
    return false
  const copy = node.cloneNode(true)
  if (removeLabel)
    copy.querySelector('strong')?.remove()
  const actualLinks = Array.from(copy.querySelectorAll('a'), link => link.getAttribute('href'))
  return sameText(renderedText(copy), markdownText(source))
    && JSON.stringify(actualLinks) === JSON.stringify(markdownLinks(source))
}

/** Count only the feature rows that the website publishes. */
export function codingAgentMatrixDimensions(features, checklist) {
  const featureCount = features.features.filter(feature => feature.showInMatrix === true).length
  return { features: featureCount, cells: featureCount * checklist.providerGroups.flat().length }
}

/** Check each published count against the complete source roster. */
function providerCountErrors(document, checklist) {
  const expected = String(checklist.providerGroups.flat().length)
  const counts = Array.from(document.querySelectorAll('[data-agent-provider-count]'))
  const errors = []
  if (counts.length === 0)
    errors.push('the page contains no generated provider count')
  for (const [index, element] of counts.entries()) {
    const actual = element.textContent.trim()
    if (actual !== expected)
      errors.push(`provider count ${index + 1} is ${JSON.stringify(actual)}, expected ${expected}`)
  }
  return errors
}

/** Check a page that publishes the provider roster without the matrix. */
export function verifyCodingAgentProviderCount(html, checklist) {
  const dom = new JSDOM(html)
  try {
    return providerCountErrors(dom.window.document, checklist)
  }
  finally {
    dom.window.close()
  }
}

/** Check that the landing page ticker renders every provider dynamically in both seamless-loop tracks. */
export function verifyCodingAgentTicker(html, checklist) {
  const dom = new JSDOM(html)
  try {
    const document = dom.window.document
    const providers = checklist.providerGroups.flat()
    const ticker = document.querySelector('.agent-ticker')
    if (!ticker)
      return ['the page contains no coding-agent ticker']
    const errors = []
    const countVar = ticker.style.getPropertyValue('--agent-ticker-count').trim()
    if (countVar !== String(providers.length))
      errors.push(`ticker count property is ${JSON.stringify(countVar)}, expected ${JSON.stringify(String(providers.length))}`)
    const lists = Array.from(ticker.querySelectorAll('.agent-ticker-list'))
    if (lists.length !== 2) {
      errors.push(`ticker has ${lists.length} tracks, expected 2`)
      return errors
    }
    const [primary, duplicate] = lists
    if (primary.getAttribute('role') !== 'list')
      errors.push('primary ticker track must have the list role')
    if (primary.getAttribute('aria-hidden') === 'true')
      errors.push('primary ticker track must not be hidden from assistive technology')
    if (duplicate.getAttribute('aria-hidden') !== 'true')
      errors.push('duplicate ticker track must be hidden from assistive technology')

    const primaryLinks = Array.from(primary.querySelectorAll('.agent-ticker-link'))
    if (primaryLinks.length !== providers.length)
      errors.push(`primary ticker track has ${primaryLinks.length} providers, expected ${providers.length}`)
    for (const [index, provider] of providers.entries()) {
      const link = primaryLinks[index]
      const lightImage = link?.querySelector('img.agent-ticker-icon-light') ?? link?.querySelector('img')
      const darkImage = link?.querySelector('img.agent-ticker-icon-dark')
      if (link?.getAttribute('href') !== provider.url)
        errors.push(`ticker provider ${index + 1} has the wrong URL`)
      if (lightImage?.getAttribute('src') !== provider.icon || lightImage?.getAttribute('alt') !== provider.label)
        errors.push(`ticker provider ${index + 1} differs from ${provider.id}`)
      if (darkImage && (darkImage.getAttribute('src') !== `/icons/agents/dark/${provider.id}.svg` || darkImage.getAttribute('alt') !== provider.label))
        errors.push(`ticker provider ${index + 1} has a mismatched dark icon`)
    }

    const duplicateLinks = Array.from(duplicate.querySelectorAll('.agent-ticker-link'))
    if (duplicateLinks.length !== providers.length)
      errors.push(`duplicate ticker track has ${duplicateLinks.length} providers, expected ${providers.length}`)
    for (const [index, provider] of providers.entries()) {
      const link = duplicateLinks[index]
      const lightImage = link?.querySelector('img.agent-ticker-icon-light') ?? link?.querySelector('img')
      const darkImage = link?.querySelector('img.agent-ticker-icon-dark')
      if (link?.getAttribute('tabindex') !== '-1')
        errors.push(`duplicate ticker provider ${index + 1} must not be keyboard focusable`)
      if (lightImage?.getAttribute('src') !== provider.icon)
        errors.push(`duplicate ticker provider ${index + 1} has the wrong icon`)
      if (lightImage?.getAttribute('alt') !== '')
        errors.push(`duplicate ticker provider ${index + 1} must have an empty alt`)
      if (darkImage && (darkImage.getAttribute('src') !== `/icons/agents/dark/${provider.id}.svg` || darkImage.getAttribute('alt') !== ''))
        errors.push(`duplicate ticker provider ${index + 1} has an invalid dark icon`)
    }
    return errors
  }
  finally {
    dom.window.close()
  }
}

/** Check the legend: one entry for each support state, with its symbol hidden from screen readers. */
function legendErrors(document, states) {
  const legend = document.querySelector('.feature-matrix-legend')
  if (!legend)
    return ['the page contains no support legend']
  const entries = Array.from(legend.children)
  const errors = []
  // The legend sets list-style to none, and Safari then drops the implicit list role.
  if (legend.getAttribute('role') !== 'list')
    errors.push('the support legend must have the list role')
  if (entries.length !== states.length)
    errors.push(`expected ${states.length} legend entries, found ${entries.length}`)
  for (const [index, state] of states.entries()) {
    const entry = entries[index]
    if (!entry)
      continue
    const expected = `${state.symbol} ${state.label}`
    if (!sameText(entry.textContent ?? '', expected))
      errors.push(`legend entry ${index + 1} reads ${JSON.stringify(normalizeText(entry.textContent ?? ''))}, expected ${JSON.stringify(expected)}`)
    if (entry.querySelector('[aria-hidden="true"]')?.textContent?.trim() !== state.symbol)
      errors.push(`legend entry ${index + 1} must hide its symbol from assistive technology`)
  }
  return errors
}

/** A whole detail note shorter than this can occur on the page by chance. */
const MIN_NOTE_LENGTH = 15
/** A sentence or a paragraph of a detail note shorter than this can occur on the page by chance. */
const MIN_PIECE_LENGTH = 40
/** The elements that the `matrix` shortcode renders. The page prose around them belongs to the docs page. */
const MATRIX_ELEMENTS = '.feature-matrix-legend, .feature-matrix, .feature-matrix-definitions, .feature-matrix-notes'

/** The text of some nodes, read twice: with and without a space between inline elements. */
function textVariants(nodes) {
  return [
    normalizeText(nodes.map(node => node.textContent ?? '').join(' ')),
    normalizeText(nodes.map(renderedText).join(' ')),
  ]
}

/**
 * The pieces of a detail note that the matrix must not hold. The pieces are each paragraph and
 * each sentence of 40 characters or more, as the plain text of the Markdown, so that a page that
 * renders the note matches.
 */
function detailPieces(detail) {
  const paragraphs = markdownBlockTexts(detail).map(normalizeText).filter(Boolean)
  const sentences = paragraphs.flatMap(paragraph => paragraph.split(/(?<=[.!?])\s+/))
  return new Set([
    ...paragraphs,
    ...sentences,
  ].filter(text => text.length >= MIN_PIECE_LENGTH))
}

/**
 * The whole detail note, as the Markdown source (a page can print it unrendered) and as the plain
 * text of the Markdown. A whole note counts from 15 characters.
 */
function wholeDetailNotes(detail) {
  return new Set([
    normalizeText(detail),
    normalizeText(markdownText(detail)),
  ].filter(text => text.length >= MIN_NOTE_LENGTH))
}

/**
 * Check that no detail note reaches the page. The website shows the user note only.
 *
 * - A whole note counts anywhere on the page.
 * - A paragraph or a sentence of 40 characters or more counts inside the matrix elements, so a
 *   copy that misses its last sentence is found. The docs page around the matrix is hand-written
 *   prose, and it can repeat a sentence of a detail note by design.
 * - Text that a published note or a feature definition also holds does not count, because the page
 *   shows that text on purpose.
 *
 * The check reads each text with and without a space between inline elements, so markup cannot hide a leak.
 */
function detailLeakErrors(document, features, checklist) {
  const pageTexts = textVariants([document.body])
  const matrixTexts = textVariants(Array.from(document.querySelectorAll(MATRIX_ELEMENTS)))
  const providers = checklist.providerGroups.flat()
  const userNotes = [
    ...providers.map(provider => provider.userNote),
    ...features.features.flatMap(feature => Object.values(checklist.cells[feature.id]).map(cell => cell.userNote)),
  ].filter(note => note?.trim())
  const publishedTexts = [
    ...userNotes.map(note => normalizeText(markdownText(note))),
    ...features.features.map(feature => normalizeText(feature.description)),
  ]
  const appearsIn = (texts, pieces) => Array.from(pieces).some(piece =>
    !publishedTexts.some(published => published.includes(piece)) && texts.some(text => text.includes(piece)))
  const leaks = detail => typeof detail === 'string'
    && (appearsIn(pageTexts, wholeDetailNotes(detail)) || appearsIn(matrixTexts, detailPieces(detail)))
  const errors = []
  for (const provider of providers) {
    if (leaks(provider.detailNote))
      errors.push(`detail note of provider ${provider.id} appears on the website`)
  }
  for (const feature of features.features) {
    for (const [providerId, cell] of Object.entries(checklist.cells[feature.id])) {
      if (leaks(cell.detailNote))
        errors.push(`detail note of ${providerId}/${feature.id} appears on the website`)
    }
  }
  return errors
}

/**
 * Check the feature groups of one table: one body for each group, a row group header
 * with the group name, repeated logos on every group after the first, and each feature
 * row under its own group. The table head already shows the logos above the first group.
 */
function featureGroupErrors(table, tableNumber, providers, publishedFeatures, groups) {
  const expectedGroups = groups.filter(group => publishedFeatures.some(feature => feature.group === group.id))
  const bodies = Array.from(table.tBodies)
  if (bodies.length !== expectedGroups.length)
    return [`table ${tableNumber} has ${bodies.length} feature groups, expected ${expectedGroups.length}`]
  const errors = []
  for (const [index, group] of expectedGroups.entries()) {
    const label = `table ${tableNumber} group ${index + 1}`
    const body = bodies[index]
    const header = body.rows[0]
    if (!header?.classList.contains('feature-group-header')) {
      errors.push(`${label} has no group header`)
      continue
    }
    const nameCell = header.children[0]
    if (nameCell?.tagName !== 'TH' || nameCell.getAttribute('scope') !== 'rowgroup')
      errors.push(`${label} has no row group header`)
    const name = nameCell?.textContent ?? ''
    if (!sameText(name, group.label))
      errors.push(`${label} is named ${JSON.stringify(name.trim())}, expected ${JSON.stringify(group.label)}`)
    const logoCells = Array.from(header.children).slice(1)
    if (index === 0) {
      if (logoCells.length > 0)
        errors.push(`${label} header must not repeat the provider logos`)
      const span = Number(nameCell?.getAttribute('colspan') ?? 1)
      if (span !== providers.length + 1)
        errors.push(`${label} header spans ${span} columns, expected ${providers.length + 1}`)
    }
    else if (logoCells.length !== providers.length) {
      errors.push(`${label} header has ${logoCells.length} logos, expected ${providers.length}`)
    }
    else {
      for (const [providerIndex, provider] of providers.entries()) {
        const logo = `${label} header logo ${providerIndex + 1}`
        const cell = logoCells[providerIndex]
        const image = cell.querySelector('img')
        if (cell.getAttribute('aria-hidden') !== 'true')
          errors.push(`${logo} must be hidden from assistive technology`)
        if (cell.querySelector('a'))
          errors.push(`${logo} must not be a link`)
        if (image?.getAttribute('alt') !== '')
          errors.push(`${logo} must have an empty alt`)
        if (image?.getAttribute('src') !== provider.icon)
          errors.push(`${logo} has the wrong icon`)
        if (!sameText(cell.querySelector('.provider-hover-label')?.textContent ?? '', provider.label))
          errors.push(`${logo} has the wrong hover label`)
      }
    }
    const actual = Array.from(body.querySelectorAll('tr:not(.feature-group-header) > th[scope="row"] a'), link => link.getAttribute('href')?.replace(/^#feature-/, ''))
    const expected = publishedFeatures.filter(feature => feature.group === group.id).map(feature => feature.id)
    if (JSON.stringify(actual) !== JSON.stringify(expected))
      errors.push(`${label} holds rows [${actual.join(', ')}], expected [${expected.join(', ')}]`)
  }
  return errors
}

/** Check the final Hugo HTML against the two source JSON files. */
export function verifyCodingAgentSite(html, features, checklist) {
  const dom = new JSDOM(html)
  const document = dom.window.document
  const errors = providerCountErrors(document, checklist)
  const groups = checklist.providerGroups
  const tables = Array.from(document.querySelectorAll('.feature-matrix table'))
  const providers = groups.flat()
  const publishedFeatures = features.features.filter(feature => feature.showInMatrix === true)
  const hiddenFeatures = features.features.filter(feature => feature.showInMatrix === false)
  const states = checklist.supportStates
  errors.push(...legendErrors(document, states))
  const tableNames = new Set()
  if (tables.length !== groups.length)
    errors.push(`expected ${groups.length} matrix tables, found ${tables.length}`)

  for (const [groupIndex, group] of groups.entries()) {
    const table = tables[groupIndex]
    if (!table)
      continue
    const labelledByIds = (table.getAttribute('aria-labelledby') ?? '').split(/\s+/)
    const labelledBy = labelledByIds.map(id => document.getElementById(id)?.textContent?.trim() ?? '').filter(Boolean).join(' ')
    const tableName = labelledBy || table.getAttribute('aria-label')?.trim() || table.querySelector('caption')?.textContent?.trim()
    if (!tableName)
      errors.push(`table ${groupIndex + 1} has no accessible name`)
    else if (tableNames.has(tableName))
      errors.push(`table ${groupIndex + 1} repeats accessible name ${tableName}`)
    else
      tableNames.add(tableName)
    // A group names a set of features on this page. The roster parts take another word.
    if (tableName && /\bgroup\b/i.test(tableName))
      errors.push(`table ${groupIndex + 1} names itself a group, but a group is a set of features`)
    const headers = Array.from(table.querySelectorAll('thead th')).slice(1)
    if (headers.length !== group.length)
      errors.push(`table ${groupIndex + 1} has ${headers.length} provider headers, expected ${group.length}`)
    for (const [providerIndex, provider] of group.entries()) {
      const image = headers[providerIndex]?.querySelector('img')
      if (image?.getAttribute('alt') !== provider.label || image?.getAttribute('src') !== provider.icon)
        errors.push(`table ${groupIndex + 1} has wrong header for ${provider.id}`)
      if (image?.closest('a')?.getAttribute('href') !== provider.url)
        errors.push(`table ${groupIndex + 1} has wrong provider URL for ${provider.id}`)
      if (!sameText(image?.closest('a')?.querySelector('.provider-hover-label')?.textContent ?? '', provider.label))
        errors.push(`table ${groupIndex + 1} has no hover label for ${provider.id}`)
      if (provider.userNote && !headers[providerIndex]?.querySelector(`a[href="#note-provider-${provider.id}"]`))
        errors.push(`table ${groupIndex + 1} omits the provider note for ${provider.id}`)
    }
    errors.push(...featureGroupErrors(table, groupIndex + 1, group, publishedFeatures, features.groups ?? []))
    const rows = Array.from(table.querySelectorAll('tbody tr:not(.feature-group-header)'))
    if (rows.length !== publishedFeatures.length)
      errors.push(`table ${groupIndex + 1} has ${rows.length} feature rows, expected ${publishedFeatures.length}`)
    for (const [featureIndex, feature] of publishedFeatures.entries()) {
      const cells = Array.from(rows[featureIndex]?.children ?? [])
      if (cells[0]?.tagName !== 'TH' || cells[0].getAttribute('scope') !== 'row')
        errors.push(`table ${groupIndex + 1} has no row header for ${feature.id}`)
      if (!sameText(cells[0]?.textContent ?? '', feature.label))
        errors.push(`table ${groupIndex + 1} has wrong label for ${feature.id}`)
      for (const [providerIndex, provider] of group.entries()) {
        const cell = checklist.cells[feature.id][provider.id]
        const rendered = cells[providerIndex + 1]
        const state = states.find(candidate => candidate.id === cell.support)
        const image = rendered?.querySelector('[role="img"]')
        const otherSymbol = states.some(other => other !== state && rendered?.textContent?.includes(other.symbol))
        if (!state || image?.getAttribute('aria-label') !== state.label || image.textContent?.trim() !== state.symbol || otherSymbol)
          errors.push(`table ${groupIndex + 1} has wrong support for ${provider.id}/${feature.id}`)
        const noteLink = rendered?.querySelector(`a[href="#note-${provider.id}-${feature.id}"]`)
        if (cell.userNote?.trim()) {
          if (!noteLink)
            errors.push(`table ${groupIndex + 1} omits the cell note for ${provider.id}/${feature.id}`)
          const note = document.getElementById(`note-${provider.id}-${feature.id}`)
          if (!noteMatches(note, cell.userNote, true))
            errors.push(`cell note for ${provider.id}/${feature.id} differs from source`)
          // The label names the state in words elsewhere, so a screen reader must skip the emoji.
          if (note?.querySelector('strong > [aria-hidden="true"]')?.textContent?.trim() !== state?.symbol)
            errors.push(`note label of ${provider.id}/${feature.id} must hide the symbol of its state from assistive technology`)
        }
        else {
          if (noteLink)
            errors.push(`table ${groupIndex + 1} links a note for ${provider.id}/${feature.id} that has no user note`)
          if (document.getElementById(`note-${provider.id}-${feature.id}`))
            errors.push(`cell note for ${provider.id}/${feature.id} appears on the website, but the cell has no user note`)
        }
      }
    }
  }

  const logos = Array.from(document.querySelectorAll('.provider-logos img:not(.lm-theme-dark)'))
  if (logos.length !== providers.length)
    errors.push(`expected ${providers.length} provider logos, found ${logos.length}`)
  for (const [index, provider] of providers.entries()) {
    const logo = logos[index]
    if (logo?.getAttribute('alt') !== provider.label || logo?.getAttribute('src') !== provider.icon)
      errors.push(`provider logo ${index + 1} differs from ${provider.id}`)
    if (logo?.closest('a')?.getAttribute('href') !== provider.url)
      errors.push(`provider logo ${index + 1} has the wrong URL`)
    if (!sameText(logo?.closest('a')?.querySelector('.provider-hover-label')?.textContent ?? '', provider.label))
      errors.push(`provider logo ${index + 1} has no hover label`)
  }

  const definitions = document.querySelectorAll('.feature-matrix-definitions dt')
  if (definitions.length !== publishedFeatures.length)
    errors.push(`expected ${publishedFeatures.length} feature definitions, found ${definitions.length}`)
  for (const feature of publishedFeatures) {
    const definition = document.getElementById(`feature-${feature.id}`)
    if (!definition || !sameText(definition.nextElementSibling?.textContent ?? '', feature.description))
      errors.push(`feature ${feature.id} lacks its exact description`)
  }
  for (const feature of hiddenFeatures) {
    if (document.getElementById(`feature-${feature.id}`))
      errors.push(`hidden feature ${feature.id} appears in feature definitions`)
    for (const provider of providers) {
      if (document.getElementById(`note-${provider.id}-${feature.id}`))
        errors.push(`hidden cell note for ${provider.id}/${feature.id} appears on the website`)
    }
  }
  for (const provider of providers) {
    if (!provider.userNote)
      continue
    const note = document.getElementById(`note-provider-${provider.id}`)
    if (!noteMatches(note, provider.userNote, true))
      errors.push(`provider note for ${provider.id} differs from source`)
  }
  errors.push(...detailLeakErrors(document, features, checklist))
  for (const link of document.querySelectorAll('.feature-matrix a[href^="#note-"]')) {
    const target = link.getAttribute('href')?.slice(1)
    if (!target || !document.getElementById(target))
      errors.push(`matrix note link has no target: ${target}`)
  }
  dom.window.close()
  return errors
}

/** Walk a directory tree and return all files ending with the given extension. */
function walkFilesByExtension(dir, extension) {
  const out = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory())
      out.push(...walkFilesByExtension(full, extension))
    else if (entry.name.endsWith(extension))
      out.push(full)
  }
  return out
}

/** Step-bearing docs pages whose H3 step headings must survive Markdown export. */
const REQUIRED_STEP_HEADINGS = [
  {
    file: 'docs/getting-started/quick-start/index.md',
    headings: [
      '### Launch LeapMux in solo mode',
      '### Create a workspace and start your first agent',
      '### Open a second agent and a shell terminal side by side',
      '### Send prompts, tune settings, and inspect diffs',
    ],
  },
  {
    file: 'docs/admin/encryption-and-data/index.md',
    headings: [
      '### Generate a new active key version',
      '### Restart the Hub',
      '### Re-encrypt existing persistent rows',
      '### Remove the retired key version (optional)',
      '### Stop the Hub and affected Workers',
      '### Restore the database and matching `encryption.key`',
      '### Start the Hub and verify Workers',
    ],
  },
  {
    file: 'docs/admin/reverse-proxy/index.md',
    headings: [
      '### Bind the Hub to loopback',
      '### Set the public URL, secure cookies, and trusted proxies',
      '### Register Workers against the external HTTPS URL',
      '### Verify HTTPS, HTTP/2, and `/metrics` protection',
      '### Register and start a Worker through the proxy',
    ],
  },
  {
    file: 'docs/admin/starting-and-stopping/index.md',
    headings: [
      '### Stop and disable the old service',
      '### Move or restore the directory',
      '### Start the instance at the new path',
    ],
  },
]

const REQUIRED_LLMS_FULL_HEADINGS = [
  '# Quick Start',
  '# Configuration Guide',
  '# Configuration Reference',
  '# Security & Authentication',
  '# Troubleshooting',
]

const REQUIRED_CONFIG_SECTIONS = [
  'hub-configuration',
  'worker-configuration',
  'storage-backends',
  'prometheus-metrics',
]

const EXPECTED_METRIC_COUNT = 21

const PRUNED_PAGEFIND_ASSETS = [
  'pagefind/pagefind-ui.js',
  'pagefind/pagefind-ui.css',
  'pagefind/pagefind-component-ui.js',
  'pagefind/pagefind-component-ui.css',
  'pagefind/pagefind-modular-ui.js',
  'pagefind/pagefind-modular-ui.css',
  'pagefind/pagefind-highlight.js',
]

const EXPECTED_ARTICLE_SECTIONS = [
  { file: 'docs/admin/configuration/index.html', expected: 'Administration' },
  { file: 'docs/recipes/parallel-agents/index.html', expected: 'Workflow Recipes' },
]

/** Find any second top-level `# <Title>` heading that duplicates the opening H1 outside fenced code blocks. */
export function findDuplicateTopH1(markdown) {
  const lines = markdown.replace(/\r\n/g, '\n').split('\n')
  let fence = null
  let firstH1 = null
  const duplicates = []

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

    const match = line.match(/^#\s+(\S.*)$/)
    if (!match)
      continue
    const heading = match[1].trim()
    if (firstH1 === null) {
      firstH1 = heading
    }
    else if (heading === firstH1) {
      duplicates.push({ line: i + 1, heading })
    }
  }
  return duplicates
}

/** Verify the five shipped reverse-proxy sample configs strip Forwarded headers and block percent-encoded /metrics. */
export function verifyReverseProxySamples(reverseProxyDir) {
  const errors = []
  const readSample = name => readFileSync(resolve(reverseProxyDir, name), 'utf8')

  const nginx = readSample('nginx.conf')
  if (!/proxy_set_header\s+Forwarded\s+""\s*;/.test(nginx))
    errors.push('reverse-proxy/nginx.conf: must strip client-supplied Forwarded header')
  if (nginx.includes('$proxy_add_x_forwarded_for') || !/proxy_set_header\s+X-Forwarded-For\s+\$remote_addr\s*;/.test(nginx))
    errors.push('reverse-proxy/nginx.conf: must set X-Forwarded-For from $remote_addr instead of $proxy_add_x_forwarded_for')

  const haproxy = readSample('haproxy.cfg')
  if (!/http-request\s+del-header\s+Forwarded\b/.test(haproxy))
    errors.push('reverse-proxy/haproxy.cfg: must strip client-supplied Forwarded header')
  if (/^\s*option\s+forwardfor\b/m.test(haproxy) || !/http-request\s+set-header\s+X-Forwarded-For\s+%\[src\]/.test(haproxy))
    errors.push('reverse-proxy/haproxy.cfg: must set X-Forwarded-For from %[src] instead of appending with option forwardfor')
  if (!/\{\s*path,url_dec\s+\/metrics\s*\}/.test(haproxy))
    errors.push('reverse-proxy/haproxy.cfg: must decode percent-encoded paths (path,url_dec /metrics) before denying /metrics')

  const apache = readSample('apache.conf')
  if (!/RequestHeader\s+unset\s+Forwarded\b/.test(apache))
    errors.push('reverse-proxy/apache.conf: must unset client-supplied Forwarded header')
  if (!/RequestHeader\s+unset\s+X-Forwarded-For\b/.test(apache))
    errors.push('reverse-proxy/apache.conf: must unset client-supplied X-Forwarded-For header')

  const caddy = readSample('Caddyfile')
  if (!/header_up\s+-Forwarded\b/.test(caddy))
    errors.push('reverse-proxy/Caddyfile: must strip client-supplied Forwarded header with header_up -Forwarded')

  const traefik = readSample('traefik-dynamic.yml')
  if (!/Forwarded:\s*""/.test(traefik) || !/middlewares:\s*\[[^\]]*strip-forwarded/.test(traefik))
    errors.push('reverse-proxy/traefik-dynamic.yml: must attach a strip-forwarded middleware clearing Forwarded')

  return errors
}

function collectHtmlIds(html) {
  const ids = new Set()
  const pattern = /\bid=(?:"([^"]+)"|'([^']+)'|([^\s"'=<>`/]+))/gi
  let match = pattern.exec(html)
  while (match !== null) {
    ids.add(match[1] ?? match[2] ?? match[3])
    match = pattern.exec(html)
  }
  return ids
}

/** Verify every internal #fragment anchor in built HTML pages resolves to a real target element ID. */
export function verifyInternalAnchors(publicDir) {
  const htmlFiles = walkFilesByExtension(publicDir, '.html')
  const pageIds = new Map()
  const pageContents = new Map()

  for (const file of htmlFiles) {
    const rel = relative(publicDir, file).replace(/\\/g, '/')
    const html = readFileSync(file, 'utf8')
    if (/<meta\s+http-equiv=["']?refresh["']?\b/i.test(html))
      continue
    const route = rel === 'index.html'
      ? '/'
      : rel.endsWith('/index.html')
        ? `/${rel.slice(0, -'index.html'.length)}`
        : `/${rel}`
    pageIds.set(route, collectHtmlIds(html))
    pageContents.set(route, html)
  }

  const errors = []
  const anchorTagPattern = /<a\s[^>]*>/gi
  const hrefAttrPattern = /\shref=(?:"([^"]+)"|'([^']+)'|([^\s"'=<>`]+))/i
  for (const [route, html] of pageContents.entries()) {
    let tagMatch = anchorTagPattern.exec(html)
    while (tagMatch !== null) {
      const hrefMatch = hrefAttrPattern.exec(tagMatch[0])
      tagMatch = anchorTagPattern.exec(html)
      if (!hrefMatch)
        continue
      const href = hrefMatch[1] ?? hrefMatch[2] ?? hrefMatch[3]
      if (!href || !href.includes('#'))
        continue
      if (/^(?:https?:|mailto:|tel:|javascript:)/i.test(href))
        continue
      const [rawPath, rawFragment] = href.split('#', 2)
      if (!rawFragment)
        continue
      let fragment = rawFragment
      try {
        fragment = decodeURIComponent(rawFragment)
      }
      catch {
        // Keep raw fragment if percent-decoding fails.
      }
      let targetRoute = null
      if (rawPath === '') {
        targetRoute = route
      }
      else if (rawPath.startsWith('/')) {
        targetRoute = rawPath.endsWith('/') || /\.[a-z0-9]+$/i.test(rawPath)
          ? rawPath
          : `${rawPath}/`
      }
      if (!targetRoute)
        continue
      const ids = pageIds.get(targetRoute)
      if (!ids) {
        errors.push(`${route}: broken internal anchor link ${JSON.stringify(href)} (target page ${targetRoute} not found)`)
      }
      else if (!ids.has(fragment)) {
        errors.push(`${route}: broken internal anchor link ${JSON.stringify(href)} (missing #${fragment} on ${targetRoute})`)
      }
    }
  }
  return errors
}

/** Find H2 headings immediately followed by another H1/H2 heading or EOF (ignoring fenced code blocks). */
export function findOrphanH2Headings(markdown) {
  const lines = markdown.replace(/\r\n/g, '\n').split('\n')
  const orphans = []
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

    if (!/^##\s+\S/.test(line))
      continue

    let nextIndex = i + 1
    while (nextIndex < lines.length && lines[nextIndex].trim() === '')
      nextIndex++

    if (nextIndex >= lines.length || /^#{1,2}\s+\S/.test(lines[nextIndex])) {
      orphans.push({
        line: i + 1,
        heading: line.trim(),
      })
    }
  }
  return orphans
}

const REQUIRED_NGINX_VARS = [
  '$http_upgrade',
  '$host',
  '$request_uri',
  '$scheme',
]

function readMetaProperty(html, property) {
  const dom = new JSDOM(html)
  try {
    return dom.window.document.querySelector(`meta[property="${property}"]`)?.getAttribute('content') ?? null
  }
  finally {
    dom.window.close()
  }
}

function externalCdnIntegrityErrors(html, file) {
  const dom = new JSDOM(html)
  const errors = []
  try {
    const doc = dom.window.document
    for (const el of doc.querySelectorAll('link[href^="https://cdn.jsdelivr.net/"], script[src^="https://cdn.jsdelivr.net/"]')) {
      const url = el.getAttribute('href') ?? el.getAttribute('src') ?? ''
      const integrity = el.getAttribute('integrity') ?? ''
      const crossorigin = el.getAttribute('crossorigin') ?? ''
      if (!integrity.startsWith('sha384-'))
        errors.push(`${file}: external CDN asset ${url} lacks sha384 integrity`)
      if (crossorigin !== 'anonymous')
        errors.push(`${file}: external CDN asset ${url} lacks crossorigin="anonymous"`)
    }
  }
  finally {
    dom.window.close()
  }
  return errors
}

function readJsonLdType(html) {
  const dom = new JSDOM(html)
  try {
    for (const script of dom.window.document.querySelectorAll('script[type="application/ld+json"]')) {
      try {
        const data = JSON.parse(script.textContent ?? '')
        const entries = Array.isArray(data?.['@graph'])
          ? data['@graph']
          : [data]
        for (const entry of entries) {
          if (entry?.['@type'] && entry['@type'] !== 'BreadcrumbList')
            return entry['@type']
        }
      }
      catch {
        return null
      }
    }
    return null
  }
  finally {
    dom.window.close()
  }
}

/** Verify built Markdown companions, llms.txt, llms-full.txt, config-reference.json, OG metadata, and Pagefind output. */
export function verifyBuiltSiteArtifacts(publicDir, checklist) {
  const errors = []
  const docsDir = resolve(publicDir, 'docs')
  const mdFiles = walkFilesByExtension(docsDir, '.md')
  if (mdFiles.length === 0)
    errors.push('no generated Markdown companions found under site/public/docs/')

  const metaTagPattern = /<meta\s[^>]*>/gi
  const metaRefreshAttrPattern = /\shttp-equiv=["']?refresh["']?(?=[\s/>])/i
  for (const htmlFile of walkFilesByExtension(docsDir, '.html')) {
    const rel = relative(publicDir, htmlFile).replace(/\\/g, '/')
    const html = readFileSync(htmlFile, 'utf8')
    const metaTags = html.match(metaTagPattern) ?? []
    if (metaTags.some(tag => metaRefreshAttrPattern.test(tag)))
      errors.push(`${rel}: contains meta-refresh redirect (Hugo alias); pre-release docs must not keep old URLs working`)
  }

  for (const file of mdFiles) {
    const rel = relative(publicDir, file).replace(/\\/g, '/')
    const text = readFileSync(file, 'utf8')
    if (text.includes('{{<') || text.includes('{{%'))
      errors.push(`${rel}: contains unexpanded Hugo shortcode`)
    if (text.includes('<!-- The command tree generates'))
      errors.push(`${rel}: leaks internal generated-file HTML comment`)
    if (rel.startsWith('docs/reference/commands/') && (text.includes('&lt;') || text.includes('&gt;')))
      errors.push(`${rel}: contains unescaped HTML angle-bracket entities`)
    for (const orphan of findOrphanH2Headings(text))
      errors.push(`${rel}: orphan empty heading ${JSON.stringify(orphan.heading)} at line ${orphan.line}`)
  }

  for (const { file, headings } of REQUIRED_STEP_HEADINGS) {
    const fullPath = resolve(publicDir, file)
    if (!existsSync(fullPath)) {
      errors.push(`${file}: missing generated Markdown companion`)
      continue
    }
    const content = readFileSync(fullPath, 'utf8')
    for (const heading of headings) {
      if (!content.includes(heading))
        errors.push(`${file}: missing expected step heading ${JSON.stringify(heading)}`)
    }
  }

  const reverseProxyMd = readFileSync(resolve(publicDir, 'docs/admin/reverse-proxy/index.md'), 'utf8')
  for (const nginxVar of REQUIRED_NGINX_VARS) {
    if (!reverseProxyMd.includes(nginxVar))
      errors.push(`docs/admin/reverse-proxy/index.md: missing nginx variable ${nginxVar}`)
  }

  const recoverPath = resolve(publicDir, 'docs/admin/recover/index.md')
  if (existsSync(recoverPath)) {
    const recoverMd = readFileSync(recoverPath, 'utf8')
    if (recoverMd.includes('> **Warning:**') && !recoverMd.includes('> Passing the top-level'))
      errors.push('docs/admin/recover/index.md: multi-paragraph callout lost blockquote prefix on continuation paragraph')
  }

  for (const rel of ['download/index.md', 'changelog/index.md']) {
    const fullPath = resolve(publicDir, rel)
    if (!existsSync(fullPath))
      continue
    const text = readFileSync(fullPath, 'utf8')
    if (/<script\b/i.test(text))
      errors.push(`${rel}: leaks raw <script> tag`)
    if (/<div\b/i.test(text))
      errors.push(`${rel}: leaks raw <div> tag`)
    for (const dup of findDuplicateTopH1(text))
      errors.push(`${rel}: duplicates top-level H1 ${JSON.stringify(dup.heading)} at line ${dup.line}`)
  }

  const downloadHtmlPath = resolve(publicDir, 'download/index.html')
  if (existsSync(downloadHtmlPath)) {
    const downloadDom = new JSDOM(readFileSync(downloadHtmlPath, 'utf8'))
    try {
      const descText = downloadDom.window.document.getElementById('lm-download-desc')?.textContent ?? ''
      if (descText.includes('`'))
        errors.push('download/index.html: #lm-download-desc contains unrendered Markdown backticks')
    }
    finally {
      downloadDom.window.close()
    }
  }

  const reverseProxyHtmlPath = resolve(publicDir, 'docs/admin/reverse-proxy/index.html')
  if (existsSync(reverseProxyHtmlPath)) {
    const reverseProxyHtml = readFileSync(reverseProxyHtmlPath, 'utf8')
    if (reverseProxyHtml.includes('<code>!Path(\\</code>'))
      errors.push('docs/admin/reverse-proxy/index.html: broken backslash-escaped backtick code span in Traefik row')
  }

  const glossaryHtmlPath = resolve(publicDir, 'docs/reference/glossary/index.html')
  if (existsSync(glossaryHtmlPath)) {
    const glossaryHtml = readFileSync(glossaryHtmlPath, 'utf8')
    if (glossaryHtml.includes('<code>Ctrl+\\``'))
      errors.push('docs/reference/glossary/index.html: broken backslash-escaped backtick code span in Quake-mode terminal')
  }

  const faqMd = readFileSync(resolve(publicDir, 'docs/reference/faq/index.md'), 'utf8')
  const firstProvider = checklist.providerGroups.flat()[0]
  if (firstProvider && !faqMd.includes(firstProvider.label))
    errors.push(`docs/reference/faq/index.md: missing expanded provider roster (expected ${firstProvider.label})`)

  const matrixMd = readFileSync(resolve(publicDir, 'docs/reference/supported-agents/index.md'), 'utf8')
  if (!matrixMd.includes('### Coding agent feature matrix'))
    errors.push('docs/reference/supported-agents/index.md: missing rendered Markdown feature matrix')

  const llmsTxt = readFileSync(resolve(publicDir, 'llms.txt'), 'utf8')
  if (!llmsTxt.includes('/docs/reference/commands/index.md'))
    errors.push('llms.txt: missing /docs/reference/commands/index.md overview entry')
  if (!llmsTxt.includes('/changelog/index.md'))
    errors.push('llms.txt: missing /changelog/index.md entry')
  if (/&amp;|&#39;|&#43;/.test(llmsTxt))
    errors.push('llms.txt: contains HTML-escaped entities')

  const llmsSmallPath = resolve(publicDir, 'llms-small.txt')
  if (existsSync(llmsSmallPath)) {
    const llmsSmall = readFileSync(llmsSmallPath, 'utf8')
    if (/&amp;|&#39;|&#43;/.test(llmsSmall))
      errors.push('llms-small.txt: contains HTML-escaped entities')
  }

  const llmsFullPath = resolve(publicDir, 'llms-full.txt')
  if (!existsSync(llmsFullPath)) {
    errors.push('site/public/llms-full.txt: missing bundled Markdown guide export')
  }
  else {
    const llmsFull = readFileSync(llmsFullPath, 'utf8')
    if (llmsFull.includes('{{<') || llmsFull.includes('{{%'))
      errors.push('llms-full.txt: contains unexpanded Hugo shortcode')
    if (!llmsFull.includes('$http_upgrade'))
      errors.push('llms-full.txt: missing nginx variable $http_upgrade')
    if (llmsFull.includes('# Control Command Reference'))
      errors.push('llms-full.txt: must exclude Control Command Reference (llms_full: false)')
    for (const heading of REQUIRED_LLMS_FULL_HEADINGS) {
      if (!llmsFull.includes(heading))
        errors.push(`llms-full.txt: missing core guide heading ${JSON.stringify(heading)}`)
    }
  }

  const configRefPath = resolve(publicDir, 'config-reference.json')
  if (!existsSync(configRefPath)) {
    errors.push('site/public/config-reference.json: missing generated configuration reference JSON')
  }
  else {
    let configRef = null
    try {
      configRef = JSON.parse(readFileSync(configRefPath, 'utf8'))
    }
    catch (err) {
      errors.push(`config-reference.json: invalid JSON (${err.message})`)
    }
    if (configRef) {
      if (configRef.schema_version !== 1)
        errors.push(`config-reference.json: schema_version is ${JSON.stringify(configRef.schema_version)}, expected 1`)
      const sections = Array.isArray(configRef.sections)
        ? configRef.sections
        : []
      for (const sectionId of REQUIRED_CONFIG_SECTIONS) {
        const section = sections.find(candidate => candidate?.id === sectionId)
        if (!section || !Array.isArray(section.groups) || section.groups.length === 0)
          errors.push(`config-reference.json: missing populated section ${JSON.stringify(sectionId)}`)
      }
      const metrics = Array.isArray(configRef.metrics)
        ? configRef.metrics
        : []
      const uniqueMetricNames = new Set(metrics.map(metric => metric?.name).filter(name => typeof name === 'string' && name.startsWith('leapmux_')))
      if (metrics.length !== EXPECTED_METRIC_COUNT || uniqueMetricNames.size !== EXPECTED_METRIC_COUNT) {
        errors.push(
          `config-reference.json: found ${uniqueMetricNames.size} unique leapmux_ metrics (${metrics.length} total), expected ${EXPECTED_METRIC_COUNT}`,
        )
      }
    }
  }

  const homeHTML = readFileSync(resolve(publicDir, 'index.html'), 'utf8')
  if (homeHTML.includes('og:image:alt" content="LeapMux — LeapMux"'))
    errors.push('index.html: og:image:alt duplicates site title')
  const ogLocale = readMetaProperty(homeHTML, 'og:locale')
  if (ogLocale !== 'en_US')
    errors.push(`index.html: og:locale is ${JSON.stringify(ogLocale)}, expected "en_US"`)
  if (/<video\b[^>]+\bautoplay\b/i.test(homeHTML))
    errors.push('index.html: contains <video autoplay> attribute; video playback must be gated by JS')
  if (!homeHTML.includes('asciinema-player-theme-leapmux') || !homeHTML.includes('--term-color-15:'))
    errors.push('index.html: missing generated asciinema-player terminal theme from contracts/theme-default.json')
  errors.push(...externalCdnIntegrityErrors(homeHTML, 'index.html'))

  const adminSectionPath = resolve(publicDir, 'docs/admin/index.html')
  if (existsSync(adminSectionPath)) {
    const schemaType = readJsonLdType(readFileSync(adminSectionPath, 'utf8'))
    if (schemaType !== 'CollectionPage')
      errors.push(`docs/admin/index.html: JSON-LD @type is ${JSON.stringify(schemaType)}, expected "CollectionPage"`)
  }

  const conceptsPath = resolve(publicDir, 'docs/getting-started/concepts/index.html')
  if (existsSync(conceptsPath))
    errors.push(...externalCdnIntegrityErrors(readFileSync(conceptsPath, 'utf8'), 'docs/getting-started/concepts/index.html'))

  const headersPath = resolve(publicDir, '_headers')
  if (existsSync(headersPath)) {
    const headers = readFileSync(headersPath, 'utf8')
    if (!/^\/\*\.txt$/m.test(headers) || !/^\/\*\.md$/m.test(headers))
      errors.push('_headers: must define disjoint /*.txt and /*.md rules')
    if (/^\/skills\/\*$/m.test(headers) || /^\/agent-guide\.md$/m.test(headers))
      errors.push('_headers: contains overlapping /skills/* or /agent-guide.md rule that duplicates Content-Type on Cloudflare')
    if (/^\s*Vary:\s*Accept\b/im.test(headers))
      errors.push('_headers: must not emit Vary: Accept for static assets without content negotiation')
  }

  for (const { file, expected } of EXPECTED_ARTICLE_SECTIONS) {
    const fullPath = resolve(publicDir, file)
    if (!existsSync(fullPath)) {
      errors.push(`${file}: missing built HTML page`)
      continue
    }
    const actual = readMetaProperty(readFileSync(fullPath, 'utf8'), 'article:section')
    if (actual !== expected)
      errors.push(`${file}: article:section is ${JSON.stringify(actual)}, expected ${JSON.stringify(expected)}`)
  }

  if (!existsSync(resolve(publicDir, 'pagefind/pagefind.js')))
    errors.push('site/public/pagefind/pagefind.js: missing Pagefind search index')
  for (const vendorAsset of ['vendor/asciinema-player/asciinema-player.min.js', 'vendor/asciinema-player/asciinema-player.css']) {
    if (!existsSync(resolve(publicDir, vendorAsset)))
      errors.push(`site/public/${vendorAsset}: missing locked asciinema-player bundle asset`)
  }
  for (const asset of PRUNED_PAGEFIND_ASSETS) {
    if (existsSync(resolve(publicDir, asset)))
      errors.push(`site/public/${asset}: unused Pagefind stock UI asset should be pruned`)
  }
  if (existsSync(resolve(publicDir, 'en.search-data.json')))
    errors.push('site/public/en.search-data.json: monolithic FlexSearch JSON should not be emitted when Pagefind is enabled')

  return errors
}

if (import.meta.main) {
  const { features, checklist } = readCodingAgentMatrix()
  const publicDir = resolve(ROOT, 'site/public')
  const html = readFileSync(resolve(publicDir, 'docs/reference/supported-agents/index.html'), 'utf8')
  const errors = verifyCodingAgentSite(html, features, checklist)
  const providerCountPages = [
    'getting-started/introduction',
    'using/coding-agents',
    'recipes/migrating',
    'reference/faq',
    'reference/glossary',
  ]
  for (const page of providerCountPages) {
    const pageHTML = readFileSync(resolve(publicDir, `docs/${page}/index.html`), 'utf8')
    errors.push(...verifyCodingAgentProviderCount(pageHTML, checklist).map(error => `${page}: ${error}`))
  }
  const homeHTML = readFileSync(resolve(publicDir, 'index.html'), 'utf8')
  errors.push(...verifyCodingAgentTicker(homeHTML, checklist).map(error => `home: ${error}`))
  errors.push(...verifyBuiltSiteArtifacts(publicDir, checklist))
  errors.push(...verifyReverseProxySamples(resolve(ROOT, 'site/content/docs/admin/reverse-proxy')))
  errors.push(...verifyInternalAnchors(publicDir))
  for (const error of errors)
    console.error(error)
  if (errors.length) {
    process.exitCode = 1
  }
  else {
    const dimensions = codingAgentMatrixDimensions(features, checklist)
    console.log(`Verified ${dimensions.features} rendered features, ${dimensions.cells} matrix cells, Markdown endpoints, and Pagefind index.`)
  }
}
