import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import process from 'node:process'
import { readCodingAgentMatrix } from './validate-coding-agent-matrix.mjs'

const ROOT = resolve(import.meta.dirname, '..')
const require = createRequire(new URL('../frontend/package.json', import.meta.url))
const { JSDOM } = require('jsdom')
const { unified } = require('unified')
const remarkParse = require('remark-parse').default

const markdown = unified().use(remarkParse)

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

function markdownText(source) {
  const visit = node => typeof node.value === 'string'
    ? node.value
    : (node.children ?? []).map(visit).join(' ')
  return visit(markdown.parse(source))
}

function markdownLinks(source) {
  const urls = []
  const visit = (node) => {
    if (node.type === 'link')
      urls.push(node.url)
    for (const child of node.children ?? [])
      visit(child)
  }
  visit(markdown.parse(source))
  return urls
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

/** Check the legend: one entry for each support state, with its symbol hidden from screen readers. */
function legendErrors(document, states) {
  const legend = document.querySelector('.feature-matrix-legend')
  if (!legend)
    return ['the page contains no support legend']
  const entries = Array.from(legend.children)
  const errors = []
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

/**
 * Check that no detail note reaches the page. The website shows the user note only. A detail
 * note that equals its user note is the user note, and a note under 15 characters can occur
 * by chance, so neither counts. The check reads the page as plain text twice, with and
 * without a space between inline elements, so markup cannot hide a leak.
 */
function detailLeakErrors(document, features, checklist) {
  const pageTexts = [normalizeText(document.body.textContent ?? ''), normalizeText(renderedText(document.body))]
  const leaks = (detail, userNote) => {
    const text = normalizeText(detail ?? '')
    return text.length >= 15 && text !== normalizeText(userNote ?? '') && pageTexts.some(page => page.includes(text))
  }
  const errors = []
  for (const provider of checklist.providerGroups.flat()) {
    if (leaks(provider.detailNote, provider.userNote))
      errors.push(`detail note of provider ${provider.id} appears on the website`)
  }
  for (const feature of features.features) {
    for (const [providerId, cell] of Object.entries(checklist.cells[feature.id])) {
      if (leaks(cell.detailNote, cell.userNote))
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

  const logos = Array.from(document.querySelectorAll('.provider-logos img'))
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

if (import.meta.main) {
  const { features, checklist } = readCodingAgentMatrix()
  const html = readFileSync(resolve(ROOT, 'site/public/docs/using/coding-agents/index.html'), 'utf8')
  const errors = verifyCodingAgentSite(html, features, checklist)
  for (const page of ['getting-started/introduction', 'reference/faq']) {
    const pageHTML = readFileSync(resolve(ROOT, `site/public/docs/${page}/index.html`), 'utf8')
    errors.push(...verifyCodingAgentProviderCount(pageHTML, checklist).map(error => `${page}: ${error}`))
  }
  for (const error of errors)
    console.error(error)
  if (errors.length) {
    process.exitCode = 1
  }
  else {
    const dimensions = codingAgentMatrixDimensions(features, checklist)
    console.log(`Verified ${dimensions.features} rendered features and ${dimensions.cells} matrix cells.`)
  }
}
