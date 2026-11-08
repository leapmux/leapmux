// The download policy for build tooling: one release-asset fetch with the
// retry rules that a transient failure needs. scripts/download-cli-tools.mjs
// and desktop/rust/scripts/patch-linuxdeploy.mjs share it, so every build
// download retries the same way.
//
// A release CDN occasionally answers a download with a gateway error; the
// installer actions that these scripts replaced retried such failures too.
import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'

const RETRYABLE_STATUSES = new Set([429, 500, 502, 503, 504])
const DOWNLOAD_RETRIES = 10
const RETRY_BASE_DELAY_MS = 1000
const RETRY_MAX_DELAY_MS = 60 * 1000

function transientDelay(ms) {
  return new Promise(resolve => setTimeout(resolve, ms))
}

/**
 * The maximum wall-clock wait that the retry schedule can spend: the sum of
 * the capped backoff maxima (1 + 2 + 4 + 8 + 16 + 32 + 60×4 seconds for the
 * ten retries). A server whose Retry-After lies beyond it is not ready
 * within the retry budget at all.
 */
export function retryBudgetMs(retries = DOWNLOAD_RETRIES) {
  let budget = 0
  for (let retry = 0; retry < retries; retry++)
    budget += Math.min(RETRY_MAX_DELAY_MS, RETRY_BASE_DELAY_MS * 2 ** retry)
  return budget
}

/**
 * The jittered exponential backoff before one retry: the exponential delay
 * split half fixed and half random (equal jitter), so jobs that hit the
 * same outage do not retry in lockstep. The delay is capped at one minute.
 */
export function retryDelay(retry, random = Math.random) {
  const exponential = RETRY_BASE_DELAY_MS * 2 ** retry
  return Math.min(RETRY_MAX_DELAY_MS, exponential / 2 + random() * (exponential / 2))
}

const DELTA_SECONDS_PATTERN = /^\d+$/
// The IMF-fixdate of RFC 7231. The lenient Date.parse fallback accepts
// shapes that no server sends and no reader may honor, so the date branch
// matches the grammar first.
const HTTP_DATE_PATTERN = /^[A-Z][a-z]{2}, \d{2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2} GMT$/

/**
 * The wait that a Retry-After header asks for, in milliseconds: delta
 * seconds or an HTTP date. Null when the header is absent or malformed, so
 * the caller falls back to the backoff.
 */
export function parseRetryAfter(header, now = Date.now()) {
  if (!header)
    return null
  const value = header.trim()
  if (DELTA_SECONDS_PATTERN.test(value))
    return Number(value) * 1000
  if (!HTTP_DATE_PATTERN.test(value))
    return null
  const date = Date.parse(value)
  if (Number.isNaN(date))
    return null
  return Math.max(0, date - now)
}

/**
 * Download one file to a destination, retrying transient failures up to
 * ten times. The delay before a retry is a jittered exponential backoff
 * capped at one minute, or the exact wait that a Retry-After header asks
 * for, which nothing truncates. A Retry-After beyond the ten-retry budget
 * means the server is not ready within the budget at all, and the download
 * gives up at once. Each scheduled retry and each give-up writes one log
 * line. The fetch follows the server's redirects.
 */
export async function downloadReleaseAsset(url, destination, {
  fetchImpl = fetch,
  sleep = transientDelay,
  random = Math.random,
  retries = DOWNLOAD_RETRIES,
} = {}) {
  const budget = retryBudgetMs(retries)
  for (let retry = 0; ; retry++) {
    let error
    let retryable = false
    let retryAfterMs = null
    try {
      const response = await fetchImpl(url, {
        redirect: 'follow',
        signal: AbortSignal.timeout(5 * 60 * 1000),
      })
      if (response.ok) {
        writeFileSync(destination, Buffer.from(await response.arrayBuffer()))
        return
      }
      error = new Error(`Downloading ${url} failed with HTTP ${response.status}.`)
      retryable = RETRYABLE_STATUSES.has(response.status)
      retryAfterMs = parseRetryAfter(response.headers.get('retry-after'))
    }
    catch (thrown) {
      error = new Error(`Downloading ${url} failed: ${thrown?.message ?? thrown}`)
      retryable = true
    }
    if (!retryable) {
      console.log(`${error.message} Giving up: the failure is not transient.`)
      throw error
    }
    if (retryAfterMs !== null && retryAfterMs > budget) {
      const reason = `The server asked to retry after ${retryAfterMs / 1000}s, beyond the ${budget / 1000}s retry budget.`
      console.log(`${error.message} Giving up: ${reason}`)
      throw new Error(`${error.message} ${reason}`)
    }
    if (retry === retries) {
      console.log(`${error.message} Giving up after ${retries} retries.`)
      throw error
    }
    const delay = retryAfterMs ?? retryDelay(retry, random)
    console.log(`${error.message} Retrying (${retry + 1}/${retries}) in ${delay}ms.`)
    await sleep(delay)
  }
}
