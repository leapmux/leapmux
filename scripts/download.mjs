// The download policy for build tooling: one file fetch with the retry rules
// that a transient failure needs. scripts/download-cli-tools.mjs downloads
// the pinned CLI tools through it.
//
// A release CDN occasionally answers a download with a gateway error. The
// installer actions that download-cli-tools.mjs replaced retried such
// failures too.
import { Buffer } from 'node:buffer'
import { renameSync, rmSync, writeFileSync } from 'node:fs'
import { setTimeout as wait } from 'node:timers/promises'
import { parseRetryAfter, TRANSIENT_STATUSES } from './http-retry.mjs'

const DOWNLOAD_RETRIES = 10
const RETRY_BASE_DELAY_MS = 1000
const RETRY_MAX_DELAY_MS = 60 * 1000
// The longest that one attempt may take, the connection and the body
// together.
const FETCH_TIMEOUT_MS = 5 * 60 * 1000
// The longest that one download may take, every attempt and every wait
// together. It holds the whole retry budget of about five minutes plus two
// attempts that run to their timeout, so a CDN that stalls every connection
// fails the job in minutes, not in an hour.
const DOWNLOAD_DEADLINE_MS = 15 * 60 * 1000

/** The capped exponential ceiling of the backoff before one retry. */
function backoffCeilingMs(retry) {
  return Math.min(RETRY_MAX_DELAY_MS, RETRY_BASE_DELAY_MS * 2 ** retry)
}

/**
 * The most that the retry schedule can wait in total: the sum of the backoff
 * ceilings (1 + 2 + 4 + 8 + 16 + 32 + 60×4 seconds for the ten retries).
 * Every wait, a Retry-After wait included, counts against it.
 */
export function retryBudgetMs(retries = DOWNLOAD_RETRIES) {
  let budget = 0
  for (let retry = 0; retry < retries; retry++)
    budget += backoffCeilingMs(retry)
  return budget
}

/**
 * The jittered exponential backoff before one retry: the capped ceiling
 * split half fixed and half random (equal jitter), so jobs that hit the
 * same outage do not retry in lockstep. The cap applies to the ceiling
 * before the split, so the retries at the cap keep their jitter too.
 */
export function retryDelay(retry, random = Math.random) {
  const ceiling = backoffCeilingMs(retry)
  return ceiling / 2 + random() * (ceiling / 2)
}

/** End a message with a period unless it already ends a sentence. */
function sentence(text) {
  return /[.!?]$/.test(text)
    ? text
    : `${text}.`
}

/**
 * Describe a thrown fetch failure. Node reports every network failure as
 * "fetch failed" and keeps the reason, such as ECONNRESET, in the cause.
 */
function describeFailure(thrown) {
  const message = thrown?.message ?? String(thrown)
  const cause = thrown?.cause?.code ?? thrown?.cause?.message
  return cause
    ? `${message} (${cause})`
    : message
}

function seconds(ms) {
  return `${Number((ms / 1000).toFixed(1))}s`
}

/**
 * Write a complete body to the destination. The body lands in a temporary
 * file first and replaces the destination in one rename, so the destination
 * never holds a partial download, and a caller may treat an existing
 * destination as complete. A failed write is not transient: it propagates
 * at once, and the temporary file is removed.
 */
function writeDownloadedFile(destination, body) {
  const partial = `${destination}.partial`
  try {
    writeFileSync(partial, body)
    renameSync(partial, destination)
  }
  catch (error) {
    rmSync(partial, { force: true })
    throw error
  }
}

/**
 * Download one file to a destination, retrying transient failures up to
 * ten times. The delay before a retry is the jittered exponential backoff,
 * capped at one minute, and never shorter than the wait that a Retry-After
 * header asks for, which nothing truncates. The download gives up at once when the
 * failure is not transient, when a wait would exceed what is left of the
 * retry budget, or when the next attempt would start after the download
 * deadline. Each scheduled retry and each give-up writes one log line. The
 * fetch follows the server's redirects. A malformed URL and a failed write
 * of the destination fail at once.
 */
export async function downloadFile(url, destination, {
  fetchImpl = fetch,
  sleep = wait,
  random = Math.random,
  retries = DOWNLOAD_RETRIES,
  now = Date.now,
  log = console.log,
} = {}) {
  // fetch reports a malformed URL as the same TypeError as a network
  // failure, so the URL is validated once, before the retry loop.
  if (!URL.canParse(url))
    throw new TypeError(`The download URL ${url} is malformed.`)
  const budget = retryBudgetMs(retries)
  const deadline = now() + DOWNLOAD_DEADLINE_MS
  let waited = 0
  for (let retry = 0; ; retry++) {
    let error
    let retryable = false
    let retryAfterMs = null
    let body = null
    try {
      const response = await fetchImpl(url, {
        redirect: 'follow',
        signal: AbortSignal.timeout(Math.max(1, Math.min(FETCH_TIMEOUT_MS, deadline - now()))),
      })
      if (response.ok) {
        body = Buffer.from(await response.arrayBuffer())
      }
      else {
        error = new Error(`Downloading ${url} failed with HTTP ${response.status}.`)
        retryable = TRANSIENT_STATUSES.has(response.status)
        retryAfterMs = parseRetryAfter(response.headers.get('retry-after'), now())
      }
    }
    catch (thrown) {
      error = new Error(sentence(`Downloading ${url} failed: ${describeFailure(thrown)}`), { cause: thrown })
      retryable = true
    }
    if (body !== null) {
      writeDownloadedFile(destination, body)
      return
    }
    if (!retryable) {
      log(`${error.message} Giving up: the failure is not transient.`)
      throw error
    }
    if (retryAfterMs !== null && retryAfterMs > budget - waited) {
      const reason = `The server asked to retry after ${seconds(retryAfterMs)}, beyond the ${seconds(budget - waited)} left of the ${seconds(budget)} retry budget.`
      log(`${error.message} Giving up: ${reason}`)
      throw new Error(`${error.message} ${reason}`, { cause: error.cause })
    }
    if (retry === retries) {
      log(`${error.message} Giving up after ${retries} ${retries === 1
        ? 'retry'
        : 'retries'}.`)
      throw error
    }
    // A Retry-After wait is a minimum: a zero or past wait must not spend the
    // retries faster than the backoff.
    const delay = Math.max(retryAfterMs ?? 0, retryDelay(retry, random))
    if (now() + delay >= deadline) {
      const reason = `The next attempt would start after the ${seconds(DOWNLOAD_DEADLINE_MS)} download deadline.`
      log(`${error.message} Giving up: ${reason}`)
      throw new Error(`${error.message} ${reason}`, { cause: error.cause })
    }
    log(`${error.message} Retrying (${retry + 1}/${retries}) in ${Math.round(delay)}ms.`)
    await sleep(delay)
    waited += delay
  }
}
