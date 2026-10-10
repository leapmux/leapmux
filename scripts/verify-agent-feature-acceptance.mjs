#!/usr/bin/env node
/**
 * verify-agent-feature-acceptance: accept a feature cell only from a complete
 * same-source report.
 *
 * A cell is accepted only when every discovered case of its spec file passes
 * on the FIRST attempt: no retries, no skips, no flaky markings. The report
 * and the discovery must identify the same frozen source (matching source
 * identity HEAD), and no two cells may name the same spec file.
 *
 * The tool also resets historical `testStatus` values before final acceptance:
 * pass --reset-matrix with the checklist path to clear every cell's testStatus
 * to `pending` while preserving support states and notes.
 */
import { accessSync, constants, writeFileSync } from 'node:fs'
import process from 'node:process'
import { discoveryCases, readJSON, readSourceIdentity, reportResults } from './agent-preservation-lib.mjs'

function firstAttemptPass(entry) {
  if (entry.attempts.length === 0)
    return false
  const [first, ...rest] = entry.attempts
  if (first.status !== 'passed')
    return false
  // A retry is a later attempt the runner attached to the same test.
  for (const attempt of rest) {
    if (attempt.status !== undefined)
      return false
  }
  for (const test of entry.spec.tests ?? []) {
    if (test.expectedStatus === 'skipped' || (test.annotations ?? []).some(annotation => annotation.type === 'skip' || annotation.type === 'fixme'))
      return false
    if ((test.results ?? []).length > 1)
      return false
  }
  return true
}

export function verifyAcceptance(checklist, report, discovery, identity) {
  const failures = []
  if (identity && discovery.identity && identity.head !== discovery.identity.head)
    failures.push(`the report's frozen source (${identity.head}) and the discovery's (${discovery.identity.head}) differ`)
  const results = reportResults(report)
  const discoveredByFile = new Map()
  for (const spec of discoveryCases(discovery)) {
    const list = discoveredByFile.get(spec.file) ?? []
    list.push(spec)
    discoveredByFile.set(spec.file, list)
  }
  const seenSpecs = new Map()
  for (const [feature, row] of Object.entries(checklist.cells ?? {})) {
    for (const [provider, cell] of Object.entries(row)) {
      const spec = cell.spec
      if (!spec)
        continue
      if (seenSpecs.has(spec))
        failures.push(`feature ${feature} of ${provider} and ${seenSpecs.get(spec)} name the same spec file ${spec}`)
      else
        seenSpecs.set(spec, `${feature} of ${provider}`)
      // Both manifests state paths relative to the e2e root; the checklist states repository paths.
      const relative = spec.replace(/^frontend\/tests\/e2e\//, '')
      const cases = discoveredByFile.get(relative)
      if (!cases || cases.length === 0) {
        failures.push(`feature ${feature} of ${provider} names ${spec}, which holds no discovered case`)
        continue
      }
      for (const entry of cases) {
        const result = results.get(entry.id)
        if (!result) {
          failures.push(`case ${entry.title} of ${spec} (${feature}/${provider}) has no report result`)
          continue
        }
        if (!firstAttemptPass(result))
          failures.push(`case ${entry.title} of ${spec} (${feature}/${provider}) does not pass on the first attempt (${result.attempts[0]?.status ?? 'no attempt'})`)
      }
    }
  }
  return failures
}

export function resetMatrixStatus(checklist) {
  const cells = checklist.cells ?? {}
  let reset = 0
  for (const row of Object.values(cells)) {
    for (const cell of Object.values(row)) {
      if (cell.testStatus === 'passed') {
        cell.testStatus = 'pending'
        cell.verified = false
        reset++
      }
    }
  }
  return reset
}

function main(argv) {
  const options = {}
  for (let i = 2; i < argv.length; i++) {
    const arg = argv[i]
    const [key, value] = arg.startsWith('--') ? [arg.slice(2), undefined] : ['_arg', arg]
    options[key] = value ?? argv[++i]
  }
  if (options['reset-matrix']) {
    const checklist = readJSON(options['reset-matrix'], 'feature checklist')
    const reset = resetMatrixStatus(checklist)
    writeFileSync(options['reset-matrix'], `${JSON.stringify(checklist, null, 2)}\n`)
    console.log(`Reset ${reset} cells to pending; support states and notes preserved.`)
    return
  }
  const reportPath = options.report
  const discoveryPath = options.discovery
  const checklistPath = options.checklist ?? 'frontend/tests/e2e/feature-matrix/checklist.json'
  if (!reportPath || !discoveryPath) {
    console.error('Usage: verify-agent-feature-acceptance --report <combined.json> --discovery <manifest.json> [--identity <identity.json>] [--checklist <file>] | --reset-matrix <file>')
    process.exitCode = 2
    return
  }
  for (const path of [reportPath, discoveryPath, checklistPath]) {
    try {
      accessSync(path, constants.R_OK)
    }
    catch {
      console.error(`Cannot read ${path}.`)
      process.exitCode = 2
      return
    }
  }
  const checklist = readJSON(checklistPath, 'feature checklist')
  const report = readJSON(reportPath, 'combined report')
  const discovery = readJSON(discoveryPath, 'discovery manifest')
  const identity = options.identity ? readSourceIdentity(options.identity) : undefined
  const failures = verifyAcceptance(checklist, report, discovery, identity)
  if (failures.length > 0) {
    console.error(`Agent feature acceptance failed with ${failures.length} problem(s):`)
    for (const failure of failures)
      console.error(`  - ${failure}`)
    process.exitCode = 1
    return
  }
  const cells = Object.values(checklist.cells ?? {}).flatMap(row => Object.values(row)).filter(cell => cell.spec)
  console.log(`Accepted ${cells.length} feature cells against the same-source report.`)
}

if (process.argv[1] && process.argv[1].endsWith('verify-agent-feature-acceptance.mjs')) {
  try {
    main(process.argv)
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
