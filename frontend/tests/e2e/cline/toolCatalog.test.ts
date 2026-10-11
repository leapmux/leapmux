import type { ClineCatalogCommand } from './toolCatalog'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { parseClineCompleteCatalog, queryClineCompleteCatalog } from './toolCatalog'

const runtime = vi.hoisted(() => ({ require: vi.fn(), environment: vi.fn() }))
vi.mock('../helpers/binaryOnPath', () => ({ requireBinary: runtime.require }))
vi.mock('../helpers/server', () => ({ hubSpawnEnv: runtime.environment }))

let directory = ''
beforeEach(() => {
  vi.resetAllMocks()
  const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'cline-catalog-unit-'))
  for (const part of ['home', 'profile', 'data', 'working'])
    mkdirSync(join(directory, part))
  runtime.require.mockReturnValue('/actual/installed/cline')
  runtime.environment.mockImplementation((environment: NodeJS.ProcessEnv) => ({ ...environment, ONLY_PRIVATE: 'yes' }))
})
afterEach(() => {
  vi.restoreAllMocks()
  rmSync(directory, { recursive: true, force: true })
})

function completeCatalog(): Record<string, unknown>[] {
  const tools = ['read_files', 'search_codebase', 'run_commands', 'editor', 'fetch_web_content', 'skills', 'ask_question', 'spawn_agent']
    .map(id => ({ id, description: `Native ${id} capability.`, defaultEnabled: id !== 'spawn_agent', headlessToolNames: [id] }))
  return [...tools, {
    id: 'teams',
    description: 'Manage native agent teams.',
    defaultEnabled: false,
    headlessToolNames: ['team_spawn_teammate', 'team_shutdown_teammate', 'team_status', 'team_task', 'team_run_task', 'team_cancel_run', 'team_list_runs', 'team_await_runs', 'team_send_message', 'team_broadcast', 'team_read_mailbox', 'team_mission_log', 'team_cleanup', 'team_create_outcome', 'team_attach_outcome_fragment', 'team_review_outcome_fragment', 'team_finalize_outcome', 'team_list_outcomes'],
  }]
}

describe('parseClineCompleteCatalog', () => {
  it('preserves disabled capabilities and every native team tool', () => {
    const catalog = parseClineCompleteCatalog(completeCatalog())
    expect(catalog.map(tool => tool.id)).toEqual(['read_files', 'search_codebase', 'run_commands', 'editor', 'fetch_web_content', 'skills', 'ask_question', 'spawn_agent', 'teams'])
    expect(catalog.find(tool => tool.id === 'spawn_agent')?.defaultEnabled).toBe(false)
    expect(catalog.find(tool => tool.id === 'teams')?.headlessToolNames).toHaveLength(18)
  })

  it('accepts the native alternate editor and optional hosted web search', () => {
    const catalog = completeCatalog()
    catalog[3] = { ...catalog[3], headlessToolNames: ['apply_patch'] }
    catalog.push({ id: 'web_search', description: 'Search through the native model service.', defaultEnabled: false, headlessToolNames: ['web_search'] })
    expect(parseClineCompleteCatalog(catalog).find(tool => tool.id === 'editor')?.headlessToolNames).toEqual(['apply_patch'])
  })

  it.each([undefined, null, {}, [], '', 0, false])('rejects an absent or malformed complete list: %j', (value) => {
    expect(() => parseClineCompleteCatalog(value)).toThrow('complete tool catalog')
  })

  it.each([
    { id: '' },
    { id: 'general_script', defaultEnabled: false },
    { description: '' },
    { description: '   ' },
    { defaultEnabled: undefined },
    { defaultEnabled: 0 },
    { headlessToolNames: [] },
    { headlessToolNames: [''] },
    { headlessToolNames: [null] },
  ])('rejects an incomplete or unaudited descriptor: %j', (override) => {
    const catalog = completeCatalog()
    catalog[0] = { ...catalog[0], ...override }
    expect(() => parseClineCompleteCatalog(catalog)).toThrow('unaudited capability or an incomplete descriptor')
  })

  it('rejects a deferred executor under an audited capability', () => {
    const catalog = completeCatalog()
    catalog[8] = { ...catalog[8], headlessToolNames: ['team_spawn_teammate', 'native_code_executor'] }
    expect(() => parseClineCompleteCatalog(catalog)).toThrow('unaudited callable tool')
  })

  it('rejects a missing disabled builtin and a duplicate identity', () => {
    const catalog = completeCatalog()
    expect(() => parseClineCompleteCatalog(catalog.filter(tool => tool.id !== 'spawn_agent'))).toThrow('omits a required builtin')
    expect(() => parseClineCompleteCatalog([...catalog, catalog[0]])).toThrow('repeats a capability')
  })

  it('rejects plugins that could supply a script executor outside the builtin table', () => {
    expect(() => parseClineCompleteCatalog([...completeCatalog(), { type: 'plugin', name: 'custom-code', description: 'Run code.', enabled: false }])).toThrow('unaudited plugin')
  })

  it('retains native text and returns independent callable name arrays', () => {
    const original = completeCatalog()
    original[0] = { ...original[0], description: 'Read Unicode text: 日本語.' }
    const result = parseClineCompleteCatalog(original)
    expect(result[0]?.description).toBe('Read Unicode text: 日本語.')
    result[0]!.headlessToolNames.push('mutated')
    expect(original[0]?.headlessToolNames).toEqual(['read_files'])
  })
})

function privateQuery() {
  return {
    workingDir: join(directory, 'working'),
    runDir: directory,
    environment: { HOME: join(directory, 'home'), CLINE_DIR: join(directory, 'profile'), CLINE_DATA_DIR: join(directory, 'data') },
  }
}

describe('queryClineCompleteCatalog', () => {
  it('runs only the native metadata command with the private environment', async () => {
    const execute = vi.fn<ClineCatalogCommand>().mockResolvedValue({ stdout: JSON.stringify(completeCatalog()), stderr: '' })
    const query = privateQuery()
    const result = await queryClineCompleteCatalog(query, execute)
    expect(result.find(tool => tool.id === 'run_commands')?.headlessToolNames).toEqual(['run_commands'])
    expect(execute).toHaveBeenCalledExactlyOnceWith('/actual/installed/cline', ['config', 'tools', '--json'], { cwd: query.workingDir, env: { ...query.environment, ONLY_PRIVATE: 'yes' }, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024, timeout: 60_000 })
    // The lookup reads the environment of the command, so it finds the executable that the command starts.
    expect(runtime.require).toHaveBeenCalledExactlyOnceWith('cline', expect.any(String), { ...query.environment, ONLY_PRIVATE: 'yes' })
  })

  it('restricts the command to the remaining whole-test time', async () => {
    vi.spyOn(Date, 'now').mockReturnValue(1000)
    const execute = vi.fn<ClineCatalogCommand>().mockResolvedValue({ stdout: JSON.stringify(completeCatalog()), stderr: '' })
    await queryClineCompleteCatalog({ ...privateQuery(), deadline: 1001 }, execute)
    expect(execute.mock.calls[0]?.[2].timeout).toBe(1)
  })

  it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY])('rejects an expired or invalid deadline before a process starts: %j', async (deadline) => {
    const execute = vi.fn<ClineCatalogCommand>()
    await expect(queryClineCompleteCatalog({ ...privateQuery(), deadline }, execute)).rejects.toThrow(/test deadline/)
    expect(execute).not.toHaveBeenCalled()
  })

  it.each(['HOME', 'CLINE_DIR', 'CLINE_DATA_DIR'])('rejects an absent private path: %s', async (key) => {
    const query = privateQuery()
    const environment: NodeJS.ProcessEnv = { ...query.environment }
    delete environment[key]
    const execute = vi.fn<ClineCatalogCommand>()
    await expect(queryClineCompleteCatalog({ ...query, environment }, execute)).rejects.toThrow(`private ${key}`)
    expect(execute).not.toHaveBeenCalled()
  })

  it('rejects an outside configuration before a process starts', async () => {
    const query = privateQuery()
    const execute = vi.fn<ClineCatalogCommand>()
    await expect(queryClineCompleteCatalog({ ...query, environment: { ...query.environment, CLINE_DIR: dirname(directory) } }, execute)).rejects.toThrow('outside the E2E run')
    expect(execute).not.toHaveBeenCalled()
  })

  it('preserves the native process failure', async () => {
    const cause = new Error('The native metadata process exited with code 7.')
    const execute = vi.fn<ClineCatalogCommand>().mockRejectedValue(cause)
    await expect(queryClineCompleteCatalog(privateQuery(), execute)).rejects.toBe(cause)
  })

  it('reports invalid native JSON and retains its parser cause', async () => {
    const execute = vi.fn<ClineCatalogCommand>().mockResolvedValue({ stdout: 'not-json', stderr: '' })
    const onReceipt = vi.fn<(receipt: { stdout: string, stderr: string }) => Promise<void>>().mockResolvedValue(undefined)
    await expect(queryClineCompleteCatalog({ ...privateQuery(), onReceipt }, execute)).rejects.toMatchObject({ message: 'The native Cline configuration returned invalid tool catalog JSON.', cause: expect.any(SyntaxError) })
    expect(onReceipt).toHaveBeenCalledExactlyOnceWith({ stdout: 'not-json', stderr: '' })
  })

  it('reports a diagnostic failure instead of discarding it', async () => {
    const cause = new Error('The native catalog receipt could not attach.')
    const execute = vi.fn<ClineCatalogCommand>().mockResolvedValue({ stdout: JSON.stringify(completeCatalog()), stderr: '' })
    const onReceipt = vi.fn<(receipt: { stdout: string, stderr: string }) => Promise<void>>().mockRejectedValue(cause)
    await expect(queryClineCompleteCatalog({ ...privateQuery(), onReceipt }, execute)).rejects.toBe(cause)
  })

  it('rejects an unavailable installed binary with the reason of the lookup before a process starts', async () => {
    const missing = new Error('The native Cline catalog requires the installed CLI. The cline on PATH is a mise shim.')
    runtime.require.mockImplementation(() => {
      throw missing
    })
    const execute = vi.fn<ClineCatalogCommand>()
    await expect(queryClineCompleteCatalog(privateQuery(), execute)).rejects.toBe(missing)
    expect(execute).not.toHaveBeenCalled()
  })
})
