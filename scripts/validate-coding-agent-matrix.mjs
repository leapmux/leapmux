import { existsSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'

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

/** A user note never names a repository path. A reader cannot use one. */
const REPOSITORY_PATH = /(?:^|[\s(`])(?:frontend|backend|contracts|scripts|site)\//
/** A user note never names a source file. A domain such as pkg.go.dev is not a file name. */
const SOURCE_FILE = /\.(?:go|tsx?|mjs)(?!\w|\.\w)/

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

/**
 * Check one user note. The website publishes it, so it names no repository path, no source
 * file and no http:// link. The check ignores the text inside a link target.
 */
function userNoteErrors(note, owner) {
  if (typeof note !== 'string' || !note)
    return []
  const errors = []
  const readable = note.replace(/\]\([^)]*\)/g, ']').replace(/https?:\/\/\S+/g, '')
  if (REPOSITORY_PATH.test(readable) || SOURCE_FILE.test(readable))
    errors.push(`user note of ${owner} holds a repository path or a source file name`)
  if (/http:\/\//i.test(note))
    errors.push(`user note of ${owner} holds an http:// link`)
  return errors
}

/**
 * Check the state and the two notes of one cell. A cell that is not supported states why in a
 * detail note (the evidence). A published limited cell also states it for the reader in a user
 * note. A hidden feature has no reader, so it has no user note.
 */
function validateCellNotes(cell, key, { published, hidden }, errors) {
  if (!SUPPORT_STATE_IDS.includes(cell.support)) {
    errors.push(`cell ${key} has unknown support ${cell.support}`)
  }
  else if (cell.support !== 'supported') {
    if (published && !cell.userNote?.trim())
      errors.push(`cell ${key} is ${cell.support} but has no user note`)
    if (!cell.detailNote?.trim())
      errors.push(`cell ${key} is ${cell.support} but has no detail note`)
  }
  if (hidden && cell.userNote?.trim())
    errors.push(`cell ${key} belongs to a hidden feature and must have no user note`)
  errors.push(...userNoteErrors(cell.userNote, `cell ${key}`))
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
    errors.push(...userNoteErrors(provider.userNote, `provider ${provider.id}`))
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
