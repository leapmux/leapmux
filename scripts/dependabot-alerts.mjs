import { nextGithubPage, readGithubJsonResponse } from './github-json.mjs'
import { ecosystemOfAdvisory } from './renovate-ecosystems.mjs'
import { dependencyLockfile } from './renovate-lockfile-versions.mjs'

// The open Dependabot alerts decide which packages may take an authorized security fix,
// for the nightly prepare step and for Required CI alike.

const REPOSITORY_API_PATH = '/repos/leapmux/leapmux'
const ALERTS_API_PATH = `${REPOSITORY_API_PATH}/dependabot/alerts`

/**
 * Read every open Dependabot alert of the repository, following each page once. The
 * token must be allowed to read the alerts: the read-only App token of the nightly run, or
 * a GITHUB_TOKEN with the `vulnerability-alerts: read` permission.
 */
export async function readAlerts(token, request = fetch) {
  if (!token)
    throw new Error('Reading the open Dependabot alerts requires a token that may read them.')
  const { data: repository } = await readGithubJsonResponse(REPOSITORY_API_PATH, { token, request, description: 'Dependabot repository read' })
  if (!Number.isSafeInteger(repository?.id) || repository.id <= 0)
    throw new Error('Dependabot returned an invalid repository identity.')
  // GitHub can give the next page under the canonical path of the repository.
  const canonicalPath = `/repositories/${repository.id}/dependabot/alerts`
  const accept = url => [ALERTS_API_PATH, canonicalPath].includes(url.pathname) && !url.searchParams.has('page')
    && url.searchParams.get('state') === 'open' && url.searchParams.get('per_page') === '100'
    && [
      url.searchParams.get('after'),
      url.searchParams.get('before'),
    ].some(Boolean)
  const alerts = []
  const seen = new Set()
  let path = `${ALERTS_API_PATH}?state=open&per_page=100`
  while (path) {
    if (seen.has(path))
      throw new Error('Dependabot pagination cycle detected.')
    seen.add(path)
    const { data: batch, headers } = await readGithubJsonResponse(path, { token, request, description: 'Dependabot alert read' })
    if (!Array.isArray(batch))
      throw new TypeError('Dependabot returned an invalid alert list.')
    alerts.push(...batch)
    path = nextGithubPage(headers.get('link'), accept)
  }
  return alerts
}

/**
 * Return the names of the packages that the open alerts report, by the path of the
 * dependency lockfile that records each package, as `Map<lockfile path, Set<name>>`. An
 * alert on an untracked file, or on a package of another ecosystem than its file's,
 * reports nothing.
 */
export function alertedPackages(alerts) {
  const alerted = new Map()
  for (const alert of alerts) {
    const pkg = alert?.security_vulnerability?.package
    const lockfile = dependencyLockfile(alert?.dependency?.manifest_path)
    if (alert?.state !== 'open' || typeof pkg?.name !== 'string' || !lockfile || ecosystemOfAdvisory(pkg.ecosystem) !== lockfile.ecosystem)
      continue
    if (!alerted.has(lockfile.path))
      alerted.set(lockfile.path, new Set())
    alerted.get(lockfile.path).add(pkg.name)
  }
  return alerted
}
