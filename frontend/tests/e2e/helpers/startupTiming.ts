/**
 * Cold-start instrumentation for the mobile LTE boot-timing spec.
 *
 * Ranks what burns wall time before the authenticated shell mounts: fonts,
 * entry JS, modulepreloaded modules, the (app) route chunk, workers, and RPCs.
 * Absolute milliseconds vary by host; the byte and phase ranking is the
 * decision input for critical-path cuts.
 */
import type { CDPSession, Page, Response } from '@playwright/test'
import type { StartupBucket } from '../../../src/lib/startupAssetBuckets'
import type { PhaseMark } from './timingFixture'
import {
  classifyStartupUrl,
  emptyStartupBucketCounts,
  STARTUP_BUCKETS,
} from '../../../src/lib/startupAssetBuckets'
import { renderTimeline } from './timingFixture'

export { classifyStartupUrl, STARTUP_BUCKETS }
export type { StartupBucket }

/**
 * Chrome "LTE" / Fast 4G-style profile: enough throttle that preload contention
 * shows up, without Slow-4G wall times that drown the ranking signal.
 *
 * downloadThroughput / uploadThroughput are bytes per second.
 */
export const LTE_NETWORK_PROFILE = {
  label: 'LTE (70ms RTT, 12 Mbps down, 3 Mbps up)',
  latency: 70,
  downloadThroughput: (12 * 1024 * 1024) / 8,
  uploadThroughput: (3 * 1024 * 1024) / 8,
} as const

/** Lighthouse Slow 4G -- optional stress profile, not the primary ranker. */
export const SLOW_4G_NETWORK_PROFILE = {
  label: 'Slow 4G (150ms RTT, 1.6 Mbps down, 750 Kbps up)',
  latency: 150,
  downloadThroughput: (1.6 * 1024 * 1024) / 8,
  uploadThroughput: (750 * 1024) / 8,
} as const

export interface NetworkProfile {
  label: string
  latency: number
  downloadThroughput: number
  uploadThroughput: number
}

export interface TrackedResource {
  url: string
  bucket: StartupBucket
  /** Transfer size in bytes when known; 0 if the browser withheld it. */
  transferSize: number
  /** Encoded body size when known. */
  encodedBodySize: number
  startTime: number
  responseEnd: number
  /** True when the response finished at or before shell_visible. */
  beforeShell: boolean
}

export interface StartupMarks {
  navStart: number
  htmlDone: number | null
  firstPaint: number | null
  firstContentfulPaint: number | null
  appDivNonempty: number | null
  loadingText: number | null
  shellVisible: number
}

export interface StartupReport {
  profileLabel: string
  marks: StartupMarks
  phases: PhaseMark[]
  resources: TrackedResource[]
  /** Bytes whose responseEnd <= shellVisible, by bucket. */
  bytesBeforeShell: Record<StartupBucket, number>
  /** Count of resources before shell, by bucket. */
  countBeforeShell: Record<StartupBucket, number>
}

/**
 * Attach CDP network emulation + cache disable. Returns the session so the
 * caller can detach after the measurement.
 */
export async function installNetworkThrottle(
  page: Page,
  profile: NetworkProfile,
): Promise<CDPSession> {
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('Network.enable')
  await cdp.send('Network.setCacheDisabled', { cacheDisabled: true })
  await cdp.send('Network.emulateNetworkConditions', {
    offline: false,
    latency: profile.latency,
    downloadThroughput: profile.downloadThroughput,
    uploadThroughput: profile.uploadThroughput,
  })
  return cdp
}

/**
 * Install document observers BEFORE navigation so the first paint and the
 * first `#app` child are not missed. Safe to call once per page.
 */
export async function installStartupObservers(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as {
      __startupMarks?: {
        navStart: number
        htmlDone: number | null
        firstPaint: number | null
        firstContentfulPaint: number | null
        appDivNonempty: number | null
        loadingText: number | null
      }
    }
    const marks = {
      navStart: performance.now(),
      htmlDone: null as number | null,
      firstPaint: null as number | null,
      firstContentfulPaint: null as number | null,
      appDivNonempty: null as number | null,
      loadingText: null as number | null,
    }
    w.__startupMarks = marks

    // Paint Timing (may already hold entries after a bfcache restore; poll once).
    const readPaints = () => {
      for (const entry of performance.getEntriesByType('paint')) {
        if (entry.name === 'first-paint' && marks.firstPaint === null)
          marks.firstPaint = entry.startTime
        if (entry.name === 'first-contentful-paint' && marks.firstContentfulPaint === null)
          marks.firstContentfulPaint = entry.startTime
      }
    }
    readPaints()
    const paintObs = new PerformanceObserver(() => readPaints())
    paintObs.observe({ type: 'paint', buffered: true })

    const noteApp = () => {
      if (marks.appDivNonempty !== null)
        return
      const app = document.getElementById('app')
      if (!app)
        return
      if (app.childNodes.length > 0 || (app.textContent ?? '').trim().length > 0)
        marks.appDivNonempty = performance.now()
    }

    const noteLoading = () => {
      if (marks.loadingText !== null)
        return
      const text = document.body?.textContent ?? ''
      if (text.includes('Loading LeapMux') || text.includes('Loading...'))
        marks.loadingText = performance.now()
    }

    const mo = new MutationObserver(() => {
      noteApp()
      noteLoading()
    })
    const arm = () => {
      noteApp()
      noteLoading()
      if (document.documentElement)
        mo.observe(document.documentElement, { childList: true, subtree: true, characterData: true })
    }
    if (document.readyState === 'loading')
      document.addEventListener('DOMContentLoaded', arm, { once: true })
    else
      arm()

    // html_done when the document navigation finishes loading the main resource.
    const nav = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined
    if (nav && nav.responseEnd > 0) {
      marks.htmlDone = nav.responseEnd
    }
    else {
      const navObs = new PerformanceObserver(() => {
        const n = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined
        if (n && n.responseEnd > 0 && marks.htmlDone === null)
          marks.htmlDone = n.responseEnd
      })
      navObs.observe({ type: 'navigation', buffered: true })
    }
  })
}

/**
 * Collect Resource Timing entries and merge Playwright Content-Length sizes.
 * `sizesBeforeShell` supplies sizes for URLs that finished before the shell
 * when Resource Timing omitted transferSize (or the entry).
 */
export async function collectStartupResources(
  page: Page,
  shellVisibleMs: number,
  playwrightSizes: Map<string, number>,
  sizesBeforeShell: Map<string, number>,
): Promise<TrackedResource[]> {
  const timed = await page.evaluate(() => {
    const out: Array<{
      url: string
      transferSize: number
      encodedBodySize: number
      startTime: number
      responseEnd: number
    }> = []
    for (const entry of performance.getEntriesByType('resource') as PerformanceResourceTiming[]) {
      out.push({
        url: entry.name,
        transferSize: entry.transferSize || 0,
        encodedBodySize: entry.encodedBodySize || 0,
        startTime: entry.startTime,
        responseEnd: entry.responseEnd,
      })
    }
    const nav = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined
    if (nav) {
      out.push({
        url: location.href,
        transferSize: nav.transferSize || 0,
        encodedBodySize: nav.encodedBodySize || 0,
        startTime: nav.startTime,
        responseEnd: nav.responseEnd,
      })
    }
    return out
  })

  const byUrl = new Map<string, TrackedResource>()
  for (const r of timed) {
    const fromPw = playwrightSizes.get(r.url) ?? 0
    const transferSize = r.transferSize > 0 ? r.transferSize : fromPw
    const encodedBodySize = r.encodedBodySize > 0 ? r.encodedBodySize : fromPw
    const beforeShell = r.responseEnd > 0 && r.responseEnd <= shellVisibleMs
    const prev = byUrl.get(r.url)
    if (prev) {
      prev.startTime = Math.min(prev.startTime, r.startTime)
      prev.responseEnd = Math.max(prev.responseEnd, r.responseEnd)
      prev.transferSize = Math.max(prev.transferSize, transferSize)
      prev.encodedBodySize = Math.max(prev.encodedBodySize, encodedBodySize)
      prev.beforeShell = prev.beforeShell || beforeShell
      continue
    }
    byUrl.set(r.url, {
      url: r.url,
      bucket: classifyStartupUrl(r.url),
      transferSize,
      encodedBodySize,
      startTime: r.startTime,
      responseEnd: r.responseEnd,
      beforeShell,
    })
  }

  for (const [url, size] of sizesBeforeShell) {
    if (byUrl.has(url))
      continue
    byUrl.set(url, {
      url,
      bucket: classifyStartupUrl(url),
      transferSize: size,
      encodedBodySize: size,
      startTime: 0,
      responseEnd: 0,
      beforeShell: true,
    })
  }

  return [...byUrl.values()]
}

/** Sum transfer sizes for resources that finished before the shell. */
export function sumBytesBeforeShell(resources: TrackedResource[]): {
  bytes: Record<StartupBucket, number>
  counts: Record<StartupBucket, number>
} {
  const bytes = emptyStartupBucketCounts()
  const counts = emptyStartupBucketCounts()
  for (const r of resources) {
    if (!r.beforeShell)
      continue
    const size = r.transferSize > 0 ? r.transferSize : r.encodedBodySize
    bytes[r.bucket] += size
    counts[r.bucket] += 1
  }
  return { bytes, counts }
}

export function buildPhaseMarks(marks: StartupMarks): PhaseMark[] {
  const rows: Array<{ name: string, tMs: number | null }> = [
    { name: 'nav_start', tMs: marks.navStart },
    { name: 'html_done', tMs: marks.htmlDone },
    { name: 'first_paint', tMs: marks.firstPaint },
    { name: 'first_contentful_paint', tMs: marks.firstContentfulPaint },
    { name: 'app_div_nonempty', tMs: marks.appDivNonempty },
    { name: 'loading_text', tMs: marks.loadingText },
    { name: 'shell_visible', tMs: marks.shellVisible },
  ]
  return rows
    .filter((r): r is { name: string, tMs: number } => r.tMs !== null && Number.isFinite(r.tMs))
    .sort((a, b) => a.tMs - b.tMs)
}

/** ASCII report: timeline + bytes-before-shell ranking. */
export function renderStartupReport(report: StartupReport): string {
  const parts: string[] = []
  parts.push(`profile: ${report.profileLabel}`)
  parts.push('')
  parts.push('=== phase timeline ===')
  parts.push(renderTimeline(report.phases))
  parts.push('')
  parts.push('=== bytes before shell_visible (ranked) ===')
  const ranked = STARTUP_BUCKETS
    .map(bucket => ({
      bucket,
      bytes: report.bytesBeforeShell[bucket],
      count: report.countBeforeShell[bucket],
    }))
    .sort((a, b) => b.bytes - a.bytes)
  const total = ranked.reduce((s, r) => s + r.bytes, 0)
  const nameWidth = Math.max(...STARTUP_BUCKETS.map(b => b.length))
  parts.push(`${'bucket'.padEnd(nameWidth)}  bytes      share   count`)
  parts.push(`${'-'.repeat(nameWidth)}  ---------  ------  -----`)
  for (const row of ranked) {
    const share = total > 0 ? `${((100 * row.bytes) / total).toFixed(1)}%` : '-'
    parts.push(
      `${row.bucket.padEnd(nameWidth)}  ${String(row.bytes).padStart(9)}  ${share.padStart(6)}  ${String(row.count).padStart(5)}`,
    )
  }
  parts.push(`${'TOTAL'.padEnd(nameWidth)}  ${String(total).padStart(9)}`)
  parts.push('')
  parts.push('=== largest resources before shell (top 15) ===')
  const top = report.resources
    .filter(r => r.beforeShell)
    .map(r => ({
      ...r,
      size: r.transferSize > 0 ? r.transferSize : r.encodedBodySize,
    }))
    .sort((a, b) => b.size - a.size)
    .slice(0, 15)
  for (const r of top) {
    const short = r.url.replace(/^https?:\/\/[^/]+/, '')
    parts.push(`  ${String(r.size).padStart(9)}  [${r.bucket}]  ${short}`)
  }
  return parts.join('\n')
}

/**
 * Listen for responses and record Content-Length keyed by URL.
 * Call before `goto`. Returns maps the collector merges into Resource Timing.
 *
 * Content-Length only -- never response.body(). Worker and grammar chunks are
 * multi-megabyte; buffering them into the test process stalls the run.
 */
export function attachResponseSizeListener(page: Page): {
  sizes: Map<string, number>
  /** Mark the wall-clock moment the shell became visible. */
  markShellVisible: () => void
  sizesBeforeShell: Map<string, number>
} {
  const sizes = new Map<string, number>()
  const sizesBeforeShell = new Map<string, number>()
  let shellAt: number | null = null

  page.on('response', (response: Response) => {
    try {
      const url = response.url()
      const cl = response.headers()['content-length']
      const size = cl ? Number(cl) : 0
      if (!Number.isFinite(size) || size <= 0)
        return
      sizes.set(url, Math.max(sizes.get(url) ?? 0, size))
      if (shellAt === null || Date.now() <= shellAt)
        sizesBeforeShell.set(url, Math.max(sizesBeforeShell.get(url) ?? 0, size))
    }
    catch {
      // Detached frame / cancelled navigation -- ignore.
    }
  })

  return {
    sizes,
    sizesBeforeShell,
    markShellVisible: () => {
      shellAt = Date.now()
    },
  }
}

/**
 * Read init-script marks and stamp shell_visible. `shellVisibleMs` is
 * performance.now() captured in the page when the shell locator became visible.
 */
export async function readStartupMarks(page: Page, shellVisibleMs: number): Promise<StartupMarks> {
  const partial = await page.evaluate(() => {
    const w = window as unknown as {
      __startupMarks?: {
        navStart: number
        htmlDone: number | null
        firstPaint: number | null
        firstContentfulPaint: number | null
        appDivNonempty: number | null
        loadingText: number | null
      }
    }
    const m = w.__startupMarks
    const nav = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined
    return {
      navStart: nav ? 0 : (m?.navStart ?? 0),
      htmlDone: m?.htmlDone ?? (nav?.responseEnd ?? null),
      firstPaint: m?.firstPaint ?? null,
      firstContentfulPaint: m?.firstContentfulPaint ?? null,
      appDivNonempty: m?.appDivNonempty ?? null,
      loadingText: m?.loadingText ?? null,
    }
  })
  return { ...partial, shellVisible: shellVisibleMs }
}
