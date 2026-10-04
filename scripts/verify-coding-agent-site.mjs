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

function sameText(actual, expected) {
  const normalize = value => value
    .replace(/[‘’]/g, '\'')
    .replace(/[“”]/g, '"')
    .replace(/[–—]/g, '-')
    .trim()
    .replace(/\s+/g, ' ')
  return normalize(actual) === normalize(expected)
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

function markdownListKinds(source) {
  const kinds = []
  const visit = (node) => {
    if (node.type === 'list')
      kinds.push(node.ordered ? 'OL' : 'UL')
    for (const child of node.children ?? [])
      visit(child)
  }
  visit(markdown.parse(source))
  return kinds
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
    && JSON.stringify(Array.from(copy.querySelectorAll('ul, ol'), list => list.tagName)) === JSON.stringify(markdownListKinds(source))
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
  const usedNoteIds = new Set(providers.flatMap(provider => provider.noteRefs ?? []))
  for (const feature of publishedFeatures) {
    for (const provider of providers) {
      for (const id of checklist.cells[feature.id][provider.id].noteRefs ?? [])
        usedNoteIds.add(id)
    }
  }
  const sharedNotes = checklist.sharedNotes.filter(note => usedNoteIds.has(note.id))
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
      for (const noteId of provider.noteRefs ?? []) {
        if (!headers[providerIndex]?.querySelector(`a[href="#note-${noteId}"]`))
          errors.push(`table ${groupIndex + 1} omits provider note ${noteId} for ${provider.id}`)
      }
      if (provider.notes && !headers[providerIndex]?.querySelector(`a[href="#note-provider-${provider.id}"]`))
        errors.push(`table ${groupIndex + 1} omits the provider note for ${provider.id}`)
    }
    const rows = Array.from(table.querySelectorAll('tbody tr'))
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
        const symbol = cell.supported ? '✅' : '❌'
        const opposite = cell.supported ? '❌' : '✅'
        if (!rendered?.textContent?.includes(symbol) || rendered.textContent.includes(opposite))
          errors.push(`table ${groupIndex + 1} has wrong support for ${provider.id}/${feature.id}`)
        for (const noteId of cell.noteRefs ?? []) {
          if (!rendered?.querySelector(`a[href="#note-${noteId}"]`))
            errors.push(`table ${groupIndex + 1} omits note ${noteId} for ${provider.id}/${feature.id}`)
        }
        if (cell.notes) {
          if (!rendered?.querySelector(`a[href="#note-${provider.id}-${feature.id}"]`))
            errors.push(`table ${groupIndex + 1} omits the cell note for ${provider.id}/${feature.id}`)
          const note = document.getElementById(`note-${provider.id}-${feature.id}`)
          if (!noteMatches(note, cell.notes, true))
            errors.push(`cell note for ${provider.id}/${feature.id} differs from source`)
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
  for (const note of sharedNotes) {
    const rendered = document.getElementById(`note-${note.id}`)
    if (!noteMatches(rendered, note.text))
      errors.push(`shared note ${note.id} differs from source`)
  }
  for (const note of checklist.sharedNotes) {
    if (!usedNoteIds.has(note.id) && document.getElementById(`note-${note.id}`))
      errors.push(`hidden-only shared note ${note.id} appears on the website`)
  }
  const renderedSharedNotes = document.querySelectorAll('.feature-matrix-notes > ol > li')
  if (renderedSharedNotes.length !== sharedNotes.length)
    errors.push(`expected ${sharedNotes.length} shared notes, found ${renderedSharedNotes.length}`)
  for (const provider of providers) {
    if (!provider.notes)
      continue
    const note = document.getElementById(`note-provider-${provider.id}`)
    if (!noteMatches(note, provider.notes, true))
      errors.push(`provider note for ${provider.id} differs from source`)
  }
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
