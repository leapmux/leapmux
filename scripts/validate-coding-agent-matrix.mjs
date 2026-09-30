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

/** Check the relationships that the two JSON Schemas cannot express. */
export function validateCodingAgentMatrix(features, checklist, providerContract, { root = ROOT, requireCellSpecs = false } = {}) {
  const errors = []
  const definitions = features.features ?? []
  const providerGroups = checklist.providerGroups ?? []
  const providers = providerGroups.flat()
  const featureIds = new Set()
  const providerIds = new Set()
  const contractLabels = new Set(Object.values(providerContract.providers ?? {}).map(provider => provider.displayName))
  const referencedNotes = new Set()
  const noteIds = new Set()
  const specPaths = new Set()

  for (const feature of definitions) {
    if (featureIds.has(feature.id))
      errors.push(`feature ID occurs twice: ${feature.id}`)
    featureIds.add(feature.id)
    if (slug(feature.label) !== feature.id)
      errors.push(`feature ${feature.id} differs from its label's spec basename`)
    if (!feature.description?.trim())
      errors.push(`feature ${feature.id} has no description`)
  }

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
    for (const id of provider.noteRefs ?? [])
      referencedNotes.add(id)
  }
  compareKeys(new Set(providers.map(provider => provider.label)), contractLabels, 'provider roster', errors)

  for (const note of checklist.sharedNotes ?? []) {
    if (noteIds.has(note.id))
      errors.push(`shared note ID occurs twice: ${note.id}`)
    noteIds.add(note.id)
  }

  const rows = checklist.cells ?? {}
  compareKeys(new Set(Object.keys(rows)), featureIds, 'feature rows', errors)
  for (const [featureId, row] of Object.entries(rows)) {
    compareKeys(new Set(Object.keys(row)), providerIds, `feature ${featureId} provider cells`, errors)
    for (const [providerId, cell] of Object.entries(row)) {
      const key = `${providerId}/${featureId}`
      if (cell.matrixVerified !== true)
        errors.push(`cell ${key} is not matrix-verified`)
      if (cell.supported && cell.testStatus !== 'passed')
        errors.push(`supported cell ${key} has no passing browser spec`)
      for (const id of cell.noteRefs ?? [])
        referencedNotes.add(id)
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

  for (const id of referencedNotes) {
    if (!noteIds.has(id))
      errors.push(`note reference ${id} has no shared note`)
  }
  for (const id of noteIds) {
    if (!referencedNotes.has(id))
      errors.push(`shared note ${id} has no provider or cell`)
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
