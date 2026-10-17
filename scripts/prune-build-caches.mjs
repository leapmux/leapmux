import { appendFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const mainRef = 'refs/heads/main'
const stateKey = 'CACHE_REPLACEMENT'
const pageSize = 100
const weekPattern = '\\d{4}-W(?:0[1-9]|[1-4]\\d|5[0-3])'
const suffixPattern = new RegExp(`^[a-f0-9]{64}(?:-(${weekPattern}))?$`)
const partitionPattern = /^(?:go-build-(?:Linux|macOS|Windows)-(?:X64|ARM64)-[\w.-]+-go\d+\.\d+\.\d+|docker-go-build-(?:amd64|arm64)-go\d+\.\d+\.\d+)-$/

function timestamp(value) {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(value))
    throw new Error('A cache timestamp is invalid.')
  const milliseconds = Date.parse(value)
  if (!Number.isFinite(milliseconds) || new Date(milliseconds).toISOString().slice(0, 19) !== value.slice(0, 19))
    throw new Error('A cache timestamp is invalid.')
  return milliseconds
}

function cacheWeek(key, prefix) {
  if (typeof key !== 'string' || !key.startsWith(prefix))
    return null
  const match = suffixPattern.exec(key.slice(prefix.length))
  if (match?.[1]?.endsWith('-W53')) {
    const year = Number(match[1].slice(0, 4))
    const firstDay = new Date(`${match[1].slice(0, 4)}-01-01T00:00:00Z`).getUTCDay()
    const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
    // Week 53 exists when the year starts on Thursday, or on Wednesday in a leap year.
    if (firstDay !== 4 && !(firstDay === 3 && leap))
      return null
  }
  return match ? { week: match[1] ?? null } : null
}

function validatePlan(plan) {
  if (!plan || typeof plan !== 'object' || Array.isArray(plan))
    throw new Error('The cache replacement plan is invalid.')
  if (typeof plan.prefix !== 'string' || !partitionPattern.test(plan.prefix))
    throw new Error('The cache partition is invalid.')
  if (!cacheWeek(plan.key, plan.prefix)?.week)
    throw new Error('The replacement key must identify this partition and a UTC week.')
  if (typeof plan.repository !== 'string' || !/^[A-Z0-9-]+\/[\w.-]+$/i.test(plan.repository)
    || ['.', '..'].includes(plan.repository.split('/')[1])) {
    throw new Error('The cache repository is invalid.')
  }
  if (plan.ref !== mainRef || !['push', 'workflow_dispatch'].includes(plan.eventName))
    throw new Error('Cache replacement requires a main-branch push or release.')
  timestamp(plan.startedAt)
  return plan
}

// Capture the exact writer inputs before compilation. Post execution must use the same plan.
export function createReplacementPlan({ key, prefix, repository, ref, eventName }, now = new Date()) {
  return validatePlan({ key, prefix, repository, ref, eventName, startedAt: now.toISOString() })
}

function validateArchive(archive) {
  if (!archive || !Number.isSafeInteger(archive.id) || archive.id <= 0
    || typeof archive.key !== 'string' || typeof archive.ref !== 'string') {
    throw new Error('A cache list entry is invalid.')
  }
  timestamp(archive.created_at)
  return archive
}

// Validate the complete list before selecting any deletion. Keep newer and unrelated archives.
export function selectArchivesToReplace(plan, archives) {
  validatePlan(plan)
  if (!Array.isArray(archives))
    throw new Error('The cache list is invalid.')
  const ids = new Set()
  for (const archive of archives) {
    validateArchive(archive)
    if (ids.has(archive.id))
      throw new Error('The cache list contains a duplicate ID.')
    ids.add(archive.id)
  }
  const currentWeek = cacheWeek(plan.key, plan.prefix).week
  // The API can omit subsecond precision. Preserve the entire registration second.
  const before = Math.floor(timestamp(plan.startedAt) / 1000) * 1000
  return archives.filter((archive) => {
    if (archive.ref !== mainRef || archive.key === plan.key || timestamp(archive.created_at) >= before)
      return false
    const generation = cacheWeek(archive.key, plan.prefix)
    return generation !== null && (generation.week === null || generation.week <= currentWeek)
  })
}

function cacheEndpoint(apiUrl, repository) {
  const url = new URL(apiUrl)
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash)
    throw new Error('The GitHub API URL is invalid.')
  url.pathname = `${url.pathname.replace(/\/$/, '')}/repos/${repository}/actions/caches`
  return url
}

// A failed list must not cause a partial replacement. Read every page before deleting by ID.
export async function pruneBuildCaches(plan, { token, apiUrl = 'https://api.github.com', fetchImpl = fetch }) {
  validatePlan(plan)
  if (typeof token !== 'string' || token.trim() === '')
    throw new Error('Cache replacement requires a job token.')
  const endpoint = cacheEndpoint(apiUrl, plan.repository)
  const request = async (url, method) => {
    try {
      return await fetchImpl(url, {
        method,
        headers: {
          'Accept': 'application/vnd.github+json',
          'Authorization': `Bearer ${token}`,
          'X-GitHub-Api-Version': '2022-11-28',
        },
        redirect: 'error',
        signal: AbortSignal.timeout(30_000),
      })
    }
    catch {
      throw new Error(`GitHub cache ${method} request failed.`)
    }
  }
  const archives = []
  const seenIds = new Set()
  for (let page = 1; ; page++) {
    const url = new URL(endpoint)
    url.search = new URLSearchParams({ ref: mainRef, key: plan.prefix, sort: 'created_at', direction: 'asc', per_page: String(pageSize), page: String(page) }).toString()
    const response = await request(url, 'GET')
    if (response.status !== 200)
      throw new Error(`GitHub cache listing failed with HTTP ${response.status}.`)
    let body
    try {
      body = await response.json()
    }
    catch {
      throw new Error('The cache list response is not valid JSON.')
    }
    if (!Array.isArray(body?.actions_caches) || body.actions_caches.length > pageSize)
      throw new Error('The cache list response is invalid.')
    for (const archive of body.actions_caches) {
      validateArchive(archive)
      if (seenIds.has(archive.id))
        throw new Error('The cache listing repeated an ID across pages.')
      seenIds.add(archive.id)
      archives.push(archive)
    }
    if (body.actions_caches.length < pageSize)
      break
  }
  const selected = selectArchivesToReplace(plan, archives)
  let deleted = 0
  for (const archive of selected) {
    const response = await request(new URL(`${endpoint.href}/${archive.id}`), 'DELETE')
    // Another cleanup can remove an archive after the list completes.
    if (response.status !== 204 && response.status !== 404)
      throw new Error(`GitHub cache deletion failed with HTTP ${response.status}.`)
    if (response.status === 204)
      deleted++
  }
  return deleted
}

// Post execution requires the exact plan that the registration entrypoint saved.
export async function runCacheReplacement(env = process.env) {
  const scope = { repository: env.GITHUB_REPOSITORY, ref: env.GITHUB_REF, eventName: env.GITHUB_EVENT_NAME }
  if (env.GITHUB_ACTIONS !== 'true')
    throw new Error('Cache replacement must run as a GitHub action.')
  const saved = env[`STATE_${stateKey}`]
  if (saved === undefined)
    throw new Error('The saved cache replacement plan is absent.')
  let plan
  try {
    plan = JSON.parse(saved)
  }
  catch {
    throw new Error('The saved cache replacement plan is invalid.')
  }
  validatePlan(plan)
  for (const field of Object.keys(scope)) {
    if (plan[field] !== scope[field])
      throw new Error('The cache replacement scope changed after registration.')
  }
  if (plan.key !== env.INPUT_KEY || plan.prefix !== env.INPUT_PREFIX)
    throw new Error('The cache replacement inputs changed after registration.')
  const removed = await pruneBuildCaches(plan, { token: env.INPUT_TOKEN, apiUrl: env.GITHUB_API_URL })
  console.log(`Removed ${removed} older build cache archives.`)
}

// Register without contacting the API. Never store the token in action state.
export function registerCacheReplacement(env = process.env) {
  if (env.GITHUB_ACTIONS !== 'true')
    throw new Error('Cache replacement must run as a GitHub action.')
  const scope = { repository: env.GITHUB_REPOSITORY, ref: env.GITHUB_REF, eventName: env.GITHUB_EVENT_NAME }
  const plan = createReplacementPlan({ ...scope, key: env.INPUT_KEY, prefix: env.INPUT_PREFIX })
  if (!env.GITHUB_STATE || !env.INPUT_TOKEN?.trim())
    throw new Error('Cache replacement requires the action state file and job token.')
  appendFileSync(env.GITHUB_STATE, `${stateKey}=${JSON.stringify(plan)}\n`)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  runCacheReplacement().catch((error) => {
    console.error(`Cache replacement failed: ${error.message}`)
    process.exitCode = 1
  })
}
