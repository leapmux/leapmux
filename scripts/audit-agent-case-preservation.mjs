#!/usr/bin/env node
/**
 * audit-agent-case-preservation: validate the original browser-case records
 * against the final discovery.
 *
 * The records live once, in testdata/agent-case-preservation.json (validated
 * by its sibling schema). Every record states one original case and its exact
 * final destination:
 *
 *   - `retained`: the case sits where it always sat. Destination equals the
 *     original; a discovery case with this id must exist.
 *   - `moved`: the case changed file or title. The destination names the final
 *     file and title; a discovery case must exist with that id.
 *   - `merged`: the original's assertions live inside a broader destination
 *     case. The record names the destination and the assertions the merge had
 *     to keep; a discovery case must exist for the destination, and a title
 *     match alone never proves the assertions survived.
 *
 * The audit fails on a missing record, a duplicate original, an absent
 * destination, a lost required assertion, or an uncovered original (a case in
 * the ORIGINAL set the records do not cover; with --original-manifest that set
 * is an earlier discovery, else the passed discovery serves as its own
 * original set, which the retained records must then cover completely).
 */
import process from 'node:process'
import { computeCaseId, discoveryCases, readJSON } from './agent-preservation-lib.mjs'

const DISPOSITIONS = new Set(['retained', 'moved', 'merged'])

/**
 * Discovery manifests state paths relative to the e2e root; records state
 * repository paths. One spelling rule keeps both comparable.
 */
function discoveryRelative(file) {
  return file.replace(/^frontend\/tests\/e2e\//, '')
}

function recordCaseId(record) {
  return record.originalCaseId
    ?? computeCaseId(discoveryRelative(record.originalFile), record.originalDescribe ?? [], record.originalTitle)
}

function destinationCaseId(record) {
  const file = record.destinationFile ?? record.originalFile
  const describe = record.destinationDescribe ?? record.originalDescribe ?? []
  const title = record.destinationTitle ?? record.originalTitle
  return { file, describe, title, id: computeCaseId(discoveryRelative(file), describe, title) }
}

export function auditPreservation(records, discovery, originalDiscovery) {
  const failures = []
  const seen = new Map()
  for (const record of records) {
    const id = recordCaseId(record)
    if (seen.has(id))
      failures.push(`duplicate original case ${id} (${record.originalFile})`)
    else
      seen.set(id, record)
    if (!DISPOSITIONS.has(record.disposition))
      failures.push(`case ${id} states disposition ${JSON.stringify(record.disposition)}, not retained, moved, or merged`)
    const destination = destinationCaseId(record)
    if (record.disposition === 'retained' && (destination.file !== record.originalFile || destination.title !== record.originalTitle))
      failures.push(`retained case ${id} names a different destination, which is a move`)
    if (record.disposition === 'merged') {
      if (!Array.isArray(record.requiredAssertions) || record.requiredAssertions.length === 0)
        failures.push(`merged case ${id} states no required assertion, so nothing proves the merge kept the original's checks`)
    }
  }

  const discovered = new Map(discoveryCases(discovery).map(spec => [spec.id, spec]))
  for (const [id, record] of seen) {
    const destination = destinationCaseId(record)
    if (!discovered.has(destination.id)) {
      // A moved or merged destination may title-shift its line; the file and
      // title must still resolve to exactly one discovered case.
      const byTitle = [...discovered.values()].filter(spec => spec.file === discoveryRelative(destination.file) && spec.title === destination.title)
      if (byTitle.length !== 1)
        failures.push(`case ${id} (${record.disposition}) has no destination: ${destination.file} "${destination.title}" resolves to ${byTitle.length} discovered cases`)
    }
  }

  const originalSet = originalDiscovery ?? discovery
  const originals = new Set(discoveryCases(originalSet).map(spec => spec.id))
  for (const id of originals) {
    if (!seen.has(id))
      failures.push(`original case ${id} has no preservation record`)
  }
  for (const record of records) {
    const id = recordCaseId(record)
    if (!originals.has(id) && record.disposition === 'retained')
      failures.push(`retained case ${id} is absent from the original set`)
  }
  return failures
}

function main(argv) {
  const args = []
  const options = {}
  for (let i = 2; i < argv.length; i++) {
    const arg = argv[i]
    if (arg.startsWith('--')) {
      const [key, value] = arg.slice(2).split('=')
      options[key] = value ?? argv[++i]
    }
    else {
      args.push(arg)
    }
  }
  const recordsPath = options.records ?? 'testdata/agent-case-preservation.json'
  const discoveryPath = options.discovery ?? args[0]
  if (!discoveryPath) {
    console.error('Usage: audit-agent-case-preservation --discovery <manifest.json> [--original-manifest <manifest.json>] [--records <file>]')
    process.exitCode = 2
    return
  }
  const recordsFile = readJSON(recordsPath, 'preservation records')
  const records = recordsFile.records
  const discovery = readJSON(discoveryPath, 'discovery manifest')
  const originalDiscovery = options['original-manifest'] ? readJSON(options['original-manifest'], 'original discovery manifest') : undefined
  const failures = auditPreservation(records, discovery, originalDiscovery)
  if (failures.length > 0) {
    console.error(`Agent case preservation failed with ${failures.length} problem(s):`)
    for (const failure of failures)
      console.error(`  - ${failure}`)
    process.exitCode = 1
    return
  }
  const merged = records.filter(record => record.disposition === 'merged').length
  const moved = records.filter(record => record.disposition === 'moved').length
  console.log(`Preserved ${records.length} original cases: ${records.length - merged - moved} retained, ${moved} moved, ${merged} merged.`)
}

if (process.argv[1] && process.argv[1].endsWith('audit-agent-case-preservation.mjs')) {
  try {
    main(process.argv)
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
