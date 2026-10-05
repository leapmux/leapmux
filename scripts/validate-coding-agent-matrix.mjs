import { existsSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { parseMarkdown, walkMarkdown } from './matrix-markdown.mjs'

const ROOT = resolve(import.meta.dirname, '..')
const MATRIX_DIR = 'frontend/tests/e2e/feature-matrix'

/** Use the same ID spelling as the provider and feature spec directories. */
function slug(value) {
  return value.toLowerCase().match(/[a-z0-9]+/g)?.join('-') ?? ''
}

/** Compare two sets and report each missing or extra member. */
function compareKeys(actual, expected, label, errors) {
  for (const key of expected) {
    if (!actual.has(key))
      errors.push(`${label} is missing ${key}`)
  }
  for (const key of actual) {
    if (!expected.has(key))
      errors.push(`${label} has unknown ${key}`)
  }
}

/**
 * Check the group of each feature. The website shows one group header above the
 * published features of each group, in the order of `groups`. A hidden feature
 * has no group, because nothing displays it.
 */
function validateFeatureGroups(features, errors) {
  const groups = features.groups ?? []
  const order = new Map()
  const labels = new Set()
  for (const group of groups) {
    if (order.has(group.id))
      errors.push(`group ID occurs twice: ${group.id}`)
    else
      order.set(group.id, order.size)
    if (typeof group.label !== 'string' || !group.label.trim())
      errors.push(`group ${group.id} has no display label`)
    else if (labels.has(group.label))
      errors.push(`group label occurs twice: ${group.label}`)
    else
      labels.add(group.label)
  }

  const used = new Set()
  let latest = -1
  for (const feature of features.features ?? []) {
    if (feature.showInMatrix === false) {
      if (feature.group !== undefined)
        errors.push(`hidden feature ${feature.id} must not have a group`)
      continue
    }
    if (feature.showInMatrix !== true)
      continue
    if (feature.group === undefined) {
      errors.push(`feature ${feature.id} has no group`)
      continue
    }
    if (!order.has(feature.group)) {
      errors.push(`feature ${feature.id} has unknown group ${feature.group}`)
      continue
    }
    used.add(feature.group)
    const index = order.get(feature.group)
    // A non-decreasing sequence keeps each group in one run and in the order of `groups`.
    if (index < latest)
      errors.push(`feature ${feature.id} (group ${feature.group}) follows a feature of a later group; order the published features by group`)
    latest = Math.max(latest, index)
  }
  for (const group of groups) {
    if (!used.has(group.id))
      errors.push(`group ${group.id} has no published feature`)
  }
}

/** The three states of a cell, in the order of the legend. */
const SUPPORT_STATE_IDS = ['supported', 'agent-limit', 'leapmux-limit']

/**
 * The directories of the repository that a user note never names. A path starts with one of
 * them. `internal` is the Go convention for a package that only the repository imports.
 */
const REPOSITORY_ROOTS = ['frontend', 'backend', 'contracts', 'scripts', 'site', 'internal', 'desktop', 'proto', 'docker', 'testdata', 'icons']
/** A repository directory, then a slash, then more path. A quotation mark or a bracket may come before it. */
const PATH_TOKEN = new RegExp(`(?<![\\w./@-])(?:${REPOSITORY_ROOTS.join('|')})(?:/[\\w.@-]*)+`, 'g')
/**
 * A user note never names a source file. A domain such as pkg.go.dev is not a file name. A brand
 * such as Node.js is not one either, so `.js` after a capitalized word does not count.
 */
const SOURCE_FILE = /\.(?:go|tsx?|jsx|[mc]ts|[mc]js|rs|py|proto)(?!\w|\.\w)|(?<!\b[A-Z][a-z]+)\.js(?!\w|\.\w)/
/** A URL that the website turns into a link by itself. Hugo enables the Goldmark linkify extension. */
const BARE_URL = /\b(?!http:)[a-z][a-z0-9+.-]*:\/\/|(?:^|\s)www\./i

/**
 * Tell a repository path from two plain words that a slash joins, as in "frontend/backend". A
 * path has three or more segments, ends in a slash, or ends in a file name with an extension.
 * A path of two plain words, such as "backend/internal", looks like the two words and passes.
 */
function isRepositoryPath(token) {
  const trimmed = token.replace(/[.@-]+$/, '')
  const segments = trimmed.split('/').filter(Boolean)
  return trimmed.endsWith('/') || segments.length > 2 || /\.\w+$/.test(segments.at(-1) ?? '')
}

/** Check that the legend lists the three fixed states, each with its own symbol and label. */
function validateSupportStates(states, errors) {
  if (JSON.stringify(states.map(state => state.id)) !== JSON.stringify(SUPPORT_STATE_IDS))
    errors.push(`supportStates must list ${SUPPORT_STATE_IDS.join(', ')} in this order`)
  for (const field of ['symbol', 'label']) {
    const seen = new Set()
    for (const state of states) {
      if (seen.has(state[field]))
        errors.push(`support state ${field} occurs twice: ${state[field]}`)
      seen.add(state[field])
    }
  }
}

/** The trimmed text of a note, or the empty string for a value that is not a string. */
function trimmedNote(note) {
  return typeof note === 'string' ? note.trim() : ''
}

/** Report a detail note that is not a string. The website never shows it, but the checks below read it. */
function detailNoteErrors(note, owner) {
  return typeof note === 'string' ? [] : [`detail note of ${owner} is not a string`]
}

/**
 * Check the parsed Markdown of a user note. The website renders it with Goldmark, which keeps raw
 * HTML (`unsafe: true` in site/hugo.yaml) and turns a bare URL into a link. So the note is one
 * paragraph of text and links, every link goes to an https:// URL, and no node names a repository
 * path or a source file. The check reads the text of each node and never a link target.
 */
function noteMarkdownErrors(note, owner) {
  const tree = parseMarkdown(note)
  const errors = new Set()
  if (tree.children.length !== 1 || tree.children[0].type !== 'paragraph')
    errors.add(`user note of ${owner} must be one paragraph of text`)
  walkMarkdown(tree, (node) => {
    switch (node.type) {
      case 'html':
        errors.add(`user note of ${owner} holds raw HTML`)
        break
      case 'image':
        errors.add(`user note of ${owner} holds an image`)
        break
      case 'link':
      case 'definition':
        // An http:// target has its own message, for the plain text of a note also.
        if (!/^https?:\/\//i.test(node.url))
          errors.add(`user note of ${owner} links to ${JSON.stringify(node.url)}, which is not an https:// URL`)
        break
      case 'text':
      case 'inlineCode':
      case 'code':
        if (node.type === 'text' && BARE_URL.test(node.value))
          errors.add(`user note of ${owner} holds a bare URL; write a Markdown link`)
        if (SOURCE_FILE.test(node.value) || Array.from(node.value.matchAll(PATH_TOKEN), match => match[0]).some(isRepositoryPath))
          errors.add(`user note of ${owner} holds a repository path or a source file name`)
        break
    }
  })
  return [...errors]
}

/**
 * Check one user note. The website publishes it, so it holds no raw HTML, no repository path, no
 * source file name, and no link except an https:// link. An empty note means that the cell has none.
 * A note of only whitespace is not empty: Hugo shows its link and an empty note.
 */
function userNoteErrors(note, owner) {
  if (typeof note !== 'string')
    return [`user note of ${owner} is not a string`]
  if (!note)
    return []
  if (!note.trim())
    return [`user note of ${owner} holds only whitespace; leave it empty`]
  const errors = noteMarkdownErrors(note, owner)
  if (/http:\/\//i.test(note))
    errors.push(`user note of ${owner} holds an http:// link`)
  return errors
}

/**
 * Check the state and the two notes of one cell. A cell that is not supported states why in a
 * detail note (the evidence), which holds more than the user note, because it carries the
 * evidence behind that note. A published limited cell also states it for the reader in a user
 * note. A hidden feature has no reader, so it has no user note.
 */
function validateCellNotes(cell, key, { published, hidden }, errors) {
  const owner = `cell ${key}`
  errors.push(...userNoteErrors(cell.userNote, owner), ...detailNoteErrors(cell.detailNote, owner))
  if (!SUPPORT_STATE_IDS.includes(cell.support)) {
    errors.push(`cell ${key} has unknown support ${cell.support}`)
  }
  else if (cell.support !== 'supported') {
    const userNote = trimmedNote(cell.userNote)
    const detailNote = trimmedNote(cell.detailNote)
    if (published && typeof cell.userNote === 'string' && !userNote)
      errors.push(`cell ${key} is ${cell.support} but has no user note`)
    if (typeof cell.detailNote === 'string' && !detailNote)
      errors.push(`cell ${key} is ${cell.support} but has no detail note`)
    else if (detailNote && detailNote.length <= userNote.length)
      errors.push(`cell ${key} is ${cell.support} but its detail note is not longer than its user note`)
  }
  if (hidden && trimmedNote(cell.userNote))
    errors.push(`cell ${key} belongs to a hidden feature and must have no user note`)
}

/** Check the relationships that the two JSON Schemas cannot express. */
export function validateCodingAgentMatrix(features, checklist, providerContract, { root = ROOT, requireCellSpecs = false } = {}) {
  const errors = []
  const definitions = features.features ?? []
  const providerGroups = checklist.providerGroups ?? []
  const providers = providerGroups.flat()
  const featureIds = new Set()
  const providerIds = new Set()
  const contractLabels = new Set(Object.values(providerContract.providers ?? {}).map(provider => provider.displayName))
  const publishedIds = new Set(definitions.filter(feature => feature.showInMatrix === true).map(feature => feature.id))
  const hiddenIds = new Set(definitions.filter(feature => feature.showInMatrix === false).map(feature => feature.id))
  const specPaths = new Set()

  for (const feature of definitions) {
    if (featureIds.has(feature.id))
      errors.push(`feature ID occurs twice: ${feature.id}`)
    featureIds.add(feature.id)
    if (typeof feature.label !== 'string' || !feature.label.trim())
      errors.push(`feature ${feature.id} has no display label`)
    if (!feature.description?.trim())
      errors.push(`feature ${feature.id} has no description`)
    if (typeof feature.showInMatrix !== 'boolean')
      errors.push(`feature ${feature.id} has no boolean showInMatrix flag`)
  }
  validateFeatureGroups(features, errors)

  for (const provider of providers) {
    if (providerIds.has(provider.id))
      errors.push(`provider ID occurs twice: ${provider.id}`)
    providerIds.add(provider.id)
    if (!contractLabels.has(provider.label))
      errors.push(`provider ${provider.id} is absent from contracts/providers.json`)
    if (slug(provider.label) !== provider.id)
      errors.push(`provider ${provider.id} differs from its contract display name`)
    const iconPath = join(root, provider.icon.replace(/^\//, ''))
    if (!existsSync(iconPath))
      errors.push(`provider ${provider.id} icon does not exist: ${provider.icon}`)
    errors.push(...userNoteErrors(provider.userNote, `provider ${provider.id}`), ...detailNoteErrors(provider.detailNote, `provider ${provider.id}`))
  }
  compareKeys(new Set(providers.map(provider => provider.label)), contractLabels, 'provider roster', errors)
  validateSupportStates(checklist.supportStates ?? [], errors)

  const rows = checklist.cells ?? {}
  compareKeys(new Set(Object.keys(rows)), featureIds, 'feature rows', errors)
  for (const [featureId, row] of Object.entries(rows)) {
    compareKeys(new Set(Object.keys(row)), providerIds, `feature ${featureId} provider cells`, errors)
    for (const [providerId, cell] of Object.entries(row)) {
      const key = `${providerId}/${featureId}`
      if (cell.verified !== true)
        errors.push(`cell ${key} is not verified`)
      validateCellNotes(cell, key, { published: publishedIds.has(featureId), hidden: hiddenIds.has(featureId) }, errors)
      if (cell.testStatus === 'passed' && !cell.spec)
        errors.push(`passed cell ${key} has no spec path`)
      if (cell.spec && !existsSync(join(root, cell.spec)))
        errors.push(`cell ${key} spec does not exist: ${cell.spec}`)
      if (requireCellSpecs) {
        const expected = `frontend/tests/e2e/${providerId}/${featureId}.spec.ts`
        if (cell.spec !== expected)
          errors.push(`cell ${key} must use ${expected}`)
        if (cell.testStatus !== 'passed')
          errors.push(`cell ${key} spec has not passed`)
        if (cell.spec && specPaths.has(cell.spec))
          errors.push(`spec path serves more than one cell: ${cell.spec}`)
        specPaths.add(cell.spec)
      }
    }
  }
  return errors
}

/** Read the two matrix data files and the canonical provider display names. */
export function readCodingAgentMatrix(root = ROOT) {
  const read = path => JSON.parse(readFileSync(join(root, path), 'utf8'))
  return {
    features: read(`${MATRIX_DIR}/features.json`),
    checklist: read(`${MATRIX_DIR}/checklist.json`),
    providerContract: read('contracts/providers.json'),
  }
}

if (import.meta.main) {
  const flags = process.argv.slice(2)
  if (flags.some(flag => flag !== '--require-cell-specs'))
    throw new Error(`Unknown argument: ${flags.find(flag => flag !== '--require-cell-specs')}`)
  const data = readCodingAgentMatrix()
  const errors = validateCodingAgentMatrix(data.features, data.checklist, data.providerContract, {
    requireCellSpecs: flags.includes('--require-cell-specs'),
  })
  for (const error of errors)
    console.error(error)
  if (errors.length)
    process.exitCode = 1
  else
    console.log(`Validated ${data.features.features.length} features and ${Object.values(data.checklist.cells).reduce((count, row) => count + Object.keys(row).length, 0)} provider cells.`)
}
