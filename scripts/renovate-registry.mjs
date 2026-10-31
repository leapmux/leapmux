// Every registry read of the Renovate scripts passes through `readRegistryJson`: one
// identity, one timeout, and one failure policy. Its default transport, `registryFetch`,
// spaces the requests to each host and retries a transient failure. A caller that injects
// its own `request`, as a test does, gets neither.

/** Space calls at least `intervalMs` apart, in the order of the calls. */
export function serialThrottle(intervalMs, { now = () => Date.now(), sleep = ms => new Promise(resolve => setTimeout(resolve, ms)) } = {}) {
  if (!Number.isSafeInteger(intervalMs) || intervalMs < 0)
    throw new Error('A throttle interval must be a nonnegative integer.')
  let last = -Infinity
  let queue = Promise.resolve()
  return (task) => {
    const run = queue.then(async () => {
      const wait = last + intervalMs - now()
      if (wait > 0)
        await sleep(wait)
      last = now()
      return task()
    })
    // A failed call must not stop the calls that follow it.
    queue = run.catch(() => {})
    return run
  }
}

/** The npm registry. */
export const NPM_REGISTRY = 'https://registry.npmjs.org'
/** The crates.io API, which allows one request per second. */
export const CRATES_API = 'https://crates.io/api/v1'
/** The crates.io sparse index. crates.io asks a client to read it before the API, and sets no rate limit for it. */
export const CRATES_INDEX = 'https://index.crates.io'

/** The minimum time between two requests to one host. */
const HOST_INTERVALS = Object.freeze({ [new URL(CRATES_API).hostname]: 1000 })
// A registry answers these statuses for an overload or an outage that ends by itself.
const TRANSIENT_STATUSES = new Set([429, 500, 502, 503, 504])
// A Retry-After delay waits at most this long, inside the deadline of the read.
const MAX_RETRY_DELAY_MS = 10000

function retryDelay(retryAfter, attempt) {
  // The registries send Retry-After in seconds. Any other form waits as an unspecified delay does.
  const seconds = retryAfter === null || retryAfter.trim() === ''
    ? Number.NaN
    : Number(retryAfter)
  return Number.isFinite(seconds) && seconds >= 0
    ? Math.min(seconds * 1000, MAX_RETRY_DELAY_MS)
    : 1000 * 2 ** attempt
}

/**
 * Create a fetch that spaces the requests to each host of `intervals`, and retries a
 * network failure and a transient status `retries` times. It waits the delay that a
 * Retry-After header gives, or one second and then two. The signal of `init` limits the
 * whole read, retries included, so an aborted read is never retried.
 */
export function createRegistryFetch({ fetch: send = fetch, intervals = HOST_INTERVALS, retries = 2, now = () => Date.now(), sleep = ms => new Promise(resolve => setTimeout(resolve, ms)) } = {}) {
  if (!Number.isSafeInteger(retries) || retries < 0)
    throw new Error('A retry count must be a nonnegative integer.')
  const throttles = new Map()
  const spaced = (url, init) => {
    const host = new URL(url).hostname
    if (intervals[host] === undefined)
      return send(url, init)
    if (!throttles.has(host))
      throttles.set(host, serialThrottle(intervals[host], { now, sleep }))
    return throttles.get(host)(() => send(url, init))
  }
  return async (url, init) => {
    for (let attempt = 0; ; attempt++) {
      let response
      try {
        response = await spaced(url, init)
      }
      catch (error) {
        if (attempt >= retries || init?.signal?.aborted)
          throw error
        await sleep(retryDelay(null, attempt))
        continue
      }
      if (!TRANSIENT_STATUSES.has(response.status) || attempt >= retries || init?.signal?.aborted)
        return response
      await sleep(retryDelay(response.headers.get('retry-after'), attempt))
    }
  }
}

/** The transport of every registry read in this process. */
export const registryFetch = createRegistryFetch()

async function readRegistry(url, { request = registryFetch, description, notFound, parse }) {
  const response = await request(url, {
    headers: { 'User-Agent': 'leapmux-dependency-maintenance' },
    signal: AbortSignal.timeout(30000),
  })
  if (response.status === 404 && notFound !== undefined)
    return notFound
  if (!response.ok)
    throw new Error(`${description} failed: ${response.status} ${url}`)
  return parse(response)
}

/**
 * Read registry JSON with one identity, failure policy, and a 30-second deadline for the
 * whole read. A 404 answer returns `notFound` when the caller supplies it, and fails otherwise.
 */
export function readRegistryJson(url, { description = 'Registry metadata read', ...options } = {}) {
  return readRegistry(url, {
    ...options,
    description,
    parse: response => response.json(),
  })
}

/** Return the sparse index URL of a crate. The index files each crate under its lowercase name. */
export function crateIndexUrl(name, index = CRATES_INDEX) {
  const file = name.toLowerCase()
  const directory = file.length <= 2
    ? String(file.length)
    : file.length === 3
      ? `3/${file[0]}`
      : `${file.slice(0, 2)}/${file.slice(2, 4)}`
  return `${index}/${directory}/${file}`
}

/**
 * Read every published version of a crate from the sparse index, as
 * `[{ version, publishedAt, yanked }]`. `publishedAt` is the index `pubtime`, the crates.io
 * publication time in whole seconds, or null when the entry has none. A crate that the
 * index lacks has no versions.
 */
export async function readCrateReleases(name, { index = CRATES_INDEX, request } = {}) {
  const text = await readRegistry(crateIndexUrl(name, index), {
    request,
    description: 'Crate index read',
    notFound: '',
    parse: response => response.text(),
  })
  return text.split('\n').filter(line => line.trim() !== '').map((line) => {
    const entry = JSON.parse(line)
    if (typeof entry?.vers !== 'string')
      throw new TypeError(`Invalid crate index entry for ${name}.`)
    return {
      version: entry.vers,
      publishedAt: typeof entry.pubtime === 'string'
        ? entry.pubtime
        : null,
      yanked: entry.yanked === true,
    }
  })
}

/** Read the publication time of each version of an npm package from its packument, as `[{ version, publishedAt }]`. */
export async function readNpmReleases(name, { request } = {}) {
  const packument = await readRegistryJson(`${NPM_REGISTRY}/${encodeURIComponent(name)}`, { request, description: 'npm packument read' })
  if (!packument?.time || typeof packument.time !== 'object')
    throw new TypeError(`Invalid npm packument: ${name}`)
  // The packument also records the times of its own creation and of its latest change.
  return Object.entries(packument.time)
    .filter(([version]) => version !== 'created' && version !== 'modified')
    .map(([version, publishedAt]) => ({ version, publishedAt }))
}
