import { renovateModule } from './renovate-engine.mjs'

const { calculateMinimumReleaseAgeMs } = await renovateModule('util/minimum-release-age.js')

/** Return the release age that the policy requires, as Renovate computes it, including any buffer. */
export function releaseAgeMs(config) {
  const age = calculateMinimumReleaseAgeMs(config)
  if (!Number.isSafeInteger(age) || age <= 0)
    throw new Error(`The minimum release age must be a positive duration: ${JSON.stringify(config.minimumReleaseAge ?? null)}`)
  return age
}

/** Return the latest publication time that the release age admits at `now`. */
export function publishTimeCutoff(ageMs, now = Date.now()) {
  if (!Number.isSafeInteger(ageMs) || ageMs <= 0 || !Number.isFinite(now))
    throw new Error('A publication cutoff requires a positive release age and a finite time.')
  return new Date(now - ageMs)
}

/**
 * Read registry JSON with one identity, timeout, and failure policy. A 404 answer returns
 * `notFound` when the caller supplies it, and fails otherwise.
 */
export async function readRegistryJson(url, { request = fetch, description = 'Registry metadata read', notFound } = {}) {
  const response = await request(url, {
    headers: { 'User-Agent': 'leapmux-dependency-maintenance' },
    signal: AbortSignal.timeout(30000),
  })
  if (response.status === 404 && notFound !== undefined)
    return notFound
  if (!response.ok)
    throw new Error(`${description} failed: ${response.status} ${url}`)
  return response.json()
}

/** Space calls to one host at least `intervalMs` apart, in the order of the calls. */
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
