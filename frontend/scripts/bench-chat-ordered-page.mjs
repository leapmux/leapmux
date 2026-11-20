import { Session } from 'node:inspector/promises'
import { OrderedChatPage } from '../src/stores/chatOrderedPage.ts'

const WINDOW_ROWS = 1200
const PAGE_ROWS = 50
const ITERATIONS = 1000
const rows = Array.from({ length: WINDOW_ROWS + PAGE_ROWS }, (_, index) => ({
  id: `row-${index}`,
  seq: BigInt(index + 1),
}))
const window = rows.slice(0, WINDOW_ROWS)
const incoming = rows.slice(WINDOW_ROWS)

function copyOnAppend() {
  let current = window
  for (const row of incoming) {
    const index = current.findLastIndex(candidate => candidate.id === row.id)
    if (index < 0) {
      current = [
        ...current,
        row,
      ]
    }
  }
  return current
}

function indexedPage() {
  const page = new OrderedChatPage(window)
  for (const row of incoming) {
    const index = page.indexOf(row.id)
    if (index === -1)
      page.append(row)
  }
  return page.finish()
}

async function measure(name, append, copiedReferences) {
  for (let iteration = 0; iteration < 100; iteration++)
    append()
  globalThis.gc?.()
  const session = new Session()
  session.connect()
  try {
    await session.post('HeapProfiler.startSampling', {
      samplingInterval: 512,
      includeObjectsCollectedByMajorGC: true,
      includeObjectsCollectedByMinorGC: true,
    })
    const started = performance.now()
    for (let iteration = 0; iteration < ITERATIONS; iteration++) {
      if (append().length !== rows.length)
        throw new Error('The benchmark did not retain the full page.')
    }
    const milliseconds = performance.now() - started
    const { profile } = await session.post('HeapProfiler.stopSampling')
    const bytes = profile.samples.reduce((total, sample) => total + sample.size, 0)
    console.log(JSON.stringify({
      name,
      pages: ITERATIONS,
      sampledBytesPerPage: Math.round(bytes / ITERATIONS),
      explicitWindowReferencesCopiedPerPage: copiedReferences,
      milliseconds: Math.round(milliseconds),
    }))
  }
  finally {
    session.disconnect()
  }
}

await measure('copy on append', copyOnAppend, PAGE_ROWS * WINDOW_ROWS + PAGE_ROWS * (PAGE_ROWS - 1) / 2)
await measure('indexed page', indexedPage, WINDOW_ROWS)
