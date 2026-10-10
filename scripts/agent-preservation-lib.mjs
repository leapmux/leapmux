/**
 * Shared readers for the agent preservation tools.
 *
 * The three validators (case preservation, feature acceptance, source freeze)
 * read the same artifacts: Playwright discovery and report manifests in the
 * exact shape `playwright --list --reporter=json` and a combined run report
 * produce, plus the repository's own JSON records. One module parses them so
 * the three tools cannot disagree about what a manifest states.
 */
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'

/** The project id the e2e tree runs under; see playwright.config.ts. */
const DISCOVERY_PROJECT_ID = 'mock-chromium'

/**
 * Walk a Playwright JSON manifest's suites and yield every spec with its file,
 * title, id, and describe path. The same walker serves discovery and report
 * manifests (a report holds `specs` with `tests` beside them).
 */
export function walkSpecs(suites, describe = []) {
  const out = []
  for (const suite of suites ?? []) {
    const path = [...describe, suite.title].filter(title => title !== '')
    for (const spec of suite.specs ?? [])
      out.push({ file: spec.file, line: spec.line, title: spec.title, id: spec.id, describe: path, tests: spec.tests })
    out.push(...walkSpecs(suite.suites, path))
  }
  return out
}

/** Read and parse a JSON file, refusing an absent or malformed one. */
export function readJSON(path, what) {
  let raw
  try {
    raw = readFileSync(path, 'utf8')
  }
  catch {
    throw new Error(`Cannot read the ${what} at ${path}.`)
  }
  try {
    return JSON.parse(raw)
  }
  catch (error) {
    throw new Error(`The ${what} at ${path} is not valid JSON: ${error.message}`)
  }
}

/** The discovery cases of a `--list --reporter=json` manifest. */
export function discoveryCases(manifest) {
  return walkSpecs(manifest.suites)
}

/** The completed results of a combined run report, keyed by case id. */
export function reportResults(report) {
  const results = new Map()
  for (const spec of walkSpecs(report.suites)) {
    const tests = spec.tests ?? []
    const attempts = tests.flatMap(test => test.results ?? [])
    if (attempts.length === 0)
      continue
    results.set(spec.id, { spec, attempts })
  }
  return results
}

/**
 * Compute one Playwright case id from its file and title path, the same way
 * the runner does: the file's sha1 prefix, then the test-id expression's
 * sha1 prefix (`[project=<id>]<posix file><title path joined by U+001E>`).
 *
 * The tools use this to state an original case's id from a record whose file
 * and title a move table preserved, so the record stays checkable against a
 * tree that no longer holds the original file.
 */
export function computeCaseId(file, describe, title, projectId = DISCOVERY_PROJECT_ID) {
  const posix = file.split('\\').join('/')
  const fileId = createHash('sha1').update(posix).digest('hex').slice(0, 20)
  const expression = `[project=${projectId}]${posix}\u001E${[...describe, title].join('\u001E')}`
  const testId = createHash('sha1').update(expression).digest('hex').slice(0, 20)
  return `${fileId}-${testId}`
}

/** The source identity facts one frozen manifest states. */
export function readSourceIdentity(path) {
  return readJSON(path, 'source identity record')
}
