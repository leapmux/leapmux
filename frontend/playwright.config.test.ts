import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

beforeEach(() => {
  vi.resetModules()
  vi.stubEnv('LEAPMUX_E2E_OUTPUT_FILE_DIR', undefined)
})

afterEach(() => {
  vi.unstubAllEnvs()
})

async function loadConfig(outputFileDir?: string) {
  vi.stubEnv('LEAPMUX_E2E_OUTPUT_FILE_DIR', outputFileDir)
  return (await import('./playwright.config')).default
}

describe('isolates shard full tool output', () => {
  it('puts attachments and default reports inside the current shard directory', async () => {
    const directory = resolve('.tmp', 'config-artifacts', 'shard-1')
    const config = await loadConfig(directory)

    expect(config.outputDir).toBe(join(directory, 'test-results'))
    expect(config.reporter).toEqual([
      ['list'],
      ['blob', { outputDir: join(directory, 'blob-report') }],
      ['json', { outputFile: join(directory, 'report.json') }],
    ])
  })

  it('keeps separate output directories for concurrent shard processes', async () => {
    const firstDirectory = resolve('.tmp', 'config-artifacts', 'shard-1')
    const secondDirectory = resolve('.tmp', 'config-artifacts', 'shard-2')
    const first = await loadConfig(firstDirectory)
    vi.resetModules()
    const second = await loadConfig(secondDirectory)

    expect(first.outputDir).toBe(join(firstDirectory, 'test-results'))
    expect(second.outputDir).toBe(join(secondDirectory, 'test-results'))
    expect(first.outputDir).not.toBe(second.outputDir)
    expect(first.reporter).not.toEqual(second.reporter)
  })

  it('preserves normal Playwright defaults without a launcher full tool output directory', async () => {
    const config = await loadConfig()

    expect(config.outputDir).toBeUndefined()
    expect(config.reporter).toBeUndefined()
  })

  it.each([
    '',
    'relative-shard-artifacts',
    `${resolve('.tmp', 'config-artifacts')}\0suffix`,
  ])('rejects an unsafe launcher artifact directory: %j', async (directory) => {
    await expect(loadConfig(directory)).rejects.toThrow('The E2E full tool output directory must be an absolute path without NUL characters.')
  })

  it('keeps the shared fixture serial within each independent shard process', async () => {
    const config = await loadConfig(resolve('.tmp', 'config-artifacts', 'shard-1'))

    expect(config.workers).toBe(1)
    expect(config.fullyParallel).toBe(false)
    expect(config.globalSetup).toBe('./tests/e2e/global-setup.ts')
    expect(config.globalTeardown).toBe('./tests/e2e/global-teardown.ts')
    expect(config.retries).toBe(0)
  })
})
