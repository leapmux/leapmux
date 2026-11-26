#!/usr/bin/env node
import process from 'node:process'
import { setTimeout as wait } from 'node:timers/promises'
import { pathToFileURL } from 'node:url'
import { nextGithubPage, readGithubJsonResponse } from './github-json.mjs'

const repositoryName = 'leapmux/leapmux'
const versionsPath = '/orgs/leapmux/packages/container/leapmux/versions'
const versionsPaginationPath = '/organizations/259782689/packages/container/leapmux/versions'
const registry = 'https://ghcr.io'
const registryPath = `/v2/${repositoryName}`
const tagsPath = `${registryPath}/tags/list`
const digestPattern = /^sha256:[0-9a-f]{64}$/
const tagPattern = /^\w[\w.-]{0,127}$/
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/
const indexTypes = ['application/vnd.oci.image.index.v1+json', 'application/vnd.docker.distribution.manifest.list.v2+json']
const imageTypes = ['application/vnd.oci.image.manifest.v1+json', 'application/vnd.docker.distribution.manifest.v2+json']
const retentionMs = 7 * 24 * 60 * 60 * 1000
const restRequestReserve = 100
const maxDeletesPerRun = 300

function remainingRequests(headers) {
  const value = headers.get('x-ratelimit-remaining')
  const remaining = Number(value)
  if (!/^\d+$/.test(value) || !Number.isSafeInteger(remaining))
    throw new Error('GitHub omitted the remaining REST request budget.')
  return remaining
}

async function listVersions(token, request) {
  const versions = []
  const ids = new Set()
  const pages = new Set()
  let remaining
  let path = `${versionsPath}?per_page=100&state=active`
  while (path) {
    if (pages.has(path))
      throw new Error('GitHub repeated a package version page.')
    pages.add(path)
    const { data, headers } = await readGithubJsonResponse(path, { token, request, description: 'Package version listing' })
    remaining = remainingRequests(headers)
    if (!Array.isArray(data))
      throw new TypeError('GitHub returned an invalid package version list.')
    for (const version of data) {
      const tags = version?.metadata?.container?.tags
      const timestamps = [version?.created_at, version?.updated_at]
      if (!Number.isSafeInteger(version?.id) || version.id <= 0 || ids.has(version.id)
        || typeof version.name !== 'string' || !digestPattern.test(version.name)
        || !Array.isArray(tags) || tags.some(tag => typeof tag !== 'string' || !tagPattern.test(tag))
        || timestamps.some(value => typeof value !== 'string' || !timestampPattern.test(value) || !Number.isFinite(Date.parse(value)))) {
        throw new Error('GitHub returned an invalid or duplicate package version.')
      }
      ids.add(version.id)
      versions.push({
        id: version.id,
        digest: version.name,
        tags,
        updatedAt: Math.max(...timestamps.map(Date.parse)),
      })
    }
    // GitHub uses the numeric organization ID in its pagination links.
    path = nextGithubPage(headers.get('link'), url => [versionsPath, versionsPaginationPath].includes(url.pathname)
      && url.searchParams.get('per_page') === '100'
      && url.searchParams.get('state') === 'active'
      && /^[1-9]\d*$/.test(url.searchParams.get('page')))
  }
  return { versions, remaining }
}

async function readRegistry(path, request, headers = {}) {
  const response = await request(`${registry}${path}`, {
    headers,
    redirect: 'error',
    signal: AbortSignal.timeout(30_000),
  })
  if (!response.ok)
    throw new Error(`Container registry read failed with HTTP ${response.status} for ${path}.`)
  return { data: await response.json(), headers: response.headers }
}

async function listTags(request, headers) {
  const tags = new Set()
  let path = `${tagsPath}?n=100`
  while (path) {
    const { data, headers: responseHeaders } = await readRegistry(path, request, headers)
    if (data?.name !== repositoryName || !Array.isArray(data.tags))
      throw new Error('The container registry returned an invalid tag list.')
    for (const tag of data.tags) {
      if (typeof tag !== 'string' || !tagPattern.test(tag) || tags.has(tag))
        throw new Error('The container registry returned an invalid or duplicate tag.')
      tags.add(tag)
    }
    const link = responseHeaders.get('link')
    if (link === null)
      break
    const next = /^<([^>]+)>;\s*rel="next"$/.exec(link)
    const cursor = next && new URL(next[1], registry)
    const last = data.tags.at(-1)
    if (!cursor || cursor.origin !== registry || cursor.pathname !== tagsPath
      || !last || cursor.searchParams.get('last') !== last || cursor.searchParams.get('n') !== '100') {
      throw new Error('The container registry returned an invalid next tag page.')
    }
    path = `${tagsPath}?${new URLSearchParams({ last, n: '100' })}`
  }
  if (tags.size === 0)
    throw new Error('The container registry returned no tags after publication.')
  return tags
}

async function readManifest(reference, request, headers) {
  const { data, headers: responseHeaders } = await readRegistry(`${registryPath}/manifests/${reference}`, request, headers)
  const digest = responseHeaders.get('docker-content-digest')
  if (!digestPattern.test(digest) || (digestPattern.test(reference) && reference !== digest) || data?.schemaVersion !== 2)
    throw new Error(`The container registry returned an invalid manifest for ${reference}.`)
  const type = data.mediaType ?? responseHeaders.get('content-type')
  let descriptors = []
  if (indexTypes.includes(type) && Array.isArray(data.manifests)) {
    descriptors = [...data.manifests]
  }
  else if (!imageTypes.includes(type) || !Array.isArray(data.layers)) {
    throw new Error(`The container registry returned an unsupported manifest for ${reference}.`)
  }
  if (data.subject !== undefined)
    descriptors.push(data.subject)
  const dependencies = descriptors.map(descriptor => descriptor?.digest)
  if (dependencies.some(value => typeof value !== 'string' || !digestPattern.test(value)))
    throw new Error(`The container registry returned an invalid manifest reference for ${reference}.`)
  return { digest, dependencies }
}

async function retainManifestGraph(roots, request) {
  // This is a public package. A pull-only token reads manifests independently of the
  // GitHub token that deletes package versions through the REST API.
  const scope = new URLSearchParams({ service: 'ghcr.io', scope: `repository:${repositoryName}:pull` })
  const { data } = await readRegistry(`/token?${scope}`, request)
  if (typeof data?.token !== 'string' || data.token === '')
    throw new Error('The container registry omitted the pull token.')
  const headers = { Authorization: `Bearer ${data.token}` }
  const tags = await listTags(request, headers)
  const manifestHeaders = {
    ...headers,
    Accept: [
      ...indexTypes,
      ...imageTypes,
    ].join(', '),
  }
  const pending = [
    ...tags,
    ...roots,
  ]
  const visited = new Set(pending)
  const retained = new Set()
  for (let offset = 0; offset < pending.length;) {
    const end = Math.min(offset + 8, pending.length)
    const batch = pending.slice(offset, end).filter(reference => !retained.has(reference))
    offset = end
    const manifests = await Promise.all(batch.map(reference => readManifest(reference, request, manifestHeaders)))
    for (const { digest, dependencies } of manifests) {
      retained.add(digest)
      for (const dependency of dependencies) {
        if (!visited.has(dependency)) {
          visited.add(dependency)
          pending.push(dependency)
        }
      }
    }
  }
  return retained
}

export async function pruneContainerVersions({ repository, token, request = fetch, now = Date.now(), pause = wait, dryRun = false }) {
  if (repository !== repositoryName || typeof token !== 'string' || token.trim() === '')
    throw new Error('Container cleanup requires the leapmux/leapmux repository and a GitHub token.')
  if (!Number.isFinite(now))
    throw new TypeError('Container cleanup requires a valid current time.')
  const cutoff = now - retentionMs
  const { versions, remaining } = await listVersions(token, request)
  const roots = new Set()
  const candidates = []
  for (const version of versions) {
    if (version.tags.length || version.updatedAt >= cutoff)
      roots.add(version.digest)
    else
      candidates.push(version)
  }
  const result = { eligible: 0, deleted: 0 }
  if (candidates.length === 0)
    return result
  const retained = await retainManifestGraph(roots, request)
  const eligible = candidates.filter(version => !retained.has(version.digest))
    .sort((a, b) => a.updatedAt - b.updatedAt || a.id - b.id)
  result.eligible = eligible.length
  if (dryRun)
    return result
  // Leave room in the repository's hourly budget for other workflows. GitHub
  // recommends at least one second between writes to avoid secondary limits.
  let budget = remaining
  for (const version of eligible.slice(0, maxDeletesPerRun)) {
    if (budget <= restRequestReserve)
      break
    if (result.deleted > 0)
      await pause(1000)
    const response = await request(`https://api.github.com${versionsPath}/${version.id}`, {
      method: 'DELETE',
      headers: {
        'Authorization': `Bearer ${token}`,
        'Accept': 'application/vnd.github+json',
        'X-GitHub-Api-Version': '2022-11-28',
      },
      redirect: 'error',
      signal: AbortSignal.timeout(30_000),
    })
    if (response.status === 429 || (response.status === 403
      && (response.headers.has('retry-after') || response.headers.get('x-ratelimit-remaining') === '0'))) {
      break
    }
    if (response.status !== 204)
      throw new Error(`Package version ${version.id} deletion failed with HTTP ${response.status}.`)
    result.deleted++
    budget = Math.min(budget - 1, remainingRequests(response.headers))
  }
  return result
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [mode, extra] = process.argv.slice(2)
    if (extra !== undefined || (mode !== undefined && mode !== '--dry-run'))
      throw new Error('Container cleanup accepts only the optional --dry-run flag.')
    const dryRun = mode === '--dry-run'
    const result = await pruneContainerVersions({ repository: process.env.GITHUB_REPOSITORY, token: process.env.GH_TOKEN, dryRun })
    if (dryRun)
      console.log(`Found ${result.eligible} eligible untagged package versions.`)
    else
      console.log(`Deleted ${result.deleted} of ${result.eligible} eligible untagged package versions.`)
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
