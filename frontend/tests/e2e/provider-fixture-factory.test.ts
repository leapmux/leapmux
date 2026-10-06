import type { ManagedNativeScenarioContext, NativeContextFixtures } from './helpers/nativeScenario'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { cliSkipFixture } from './provider-fixture-factory'

describe('cliSkipFixture', () => {
  /** Run the fixture callback with a recording test info, as Playwright runs an automatic fixture. */
  async function run(reason: string | null) {
    const [fixture, options] = cliSkipFixture(reason)
    const events: string[] = []
    const testInfo = {
      skip: vi.fn((condition: boolean, description: string) => {
        events.push(`skip:${condition}:${description}`)
        // Playwright ends the test from inside `skip` with a thrown marker when the condition holds.
        if (condition)
          throw new Error(`skipped: ${description}`)
      }),
    }
    const use = vi.fn(async () => {
      events.push('use')
    })
    const outcome = await (fixture as unknown as (args: object, use: () => Promise<void>, info: typeof testInfo) => Promise<void>)({}, use, testInfo)
      .then(() => 'ran', (error: unknown) => (error as Error).message)
    return { options, events, outcome }
  }

  it('registers an automatic fixture, so it runs before the fixtures that start an agent', () => {
    expect(cliSkipFixture(null)[1]).toEqual({ auto: true })
  })

  it('skips with the reason before the test uses any fixture when the CLI is missing', async () => {
    const { events, outcome } = await run('Amp E2E requires the amp CLI on PATH')
    expect(events).toEqual(['skip:true:Amp E2E requires the amp CLI on PATH'])
    expect(outcome).toBe('skipped: Amp E2E requires the amp CLI on PATH')
  })

  it('runs the test when the CLI is present', async () => {
    const { events, outcome } = await run(null)
    expect(events).toEqual(['skip:false:', 'use'])
    expect(outcome).toBe('ran')
  })

  it.each(['', '  \n'])('refuses an empty reason, which would skip without saying why: %j', (reason) => {
    expect(() => cliSkipFixture(reason)).toThrow('needs the reason')
  })
})

/** The facts of one provider test object that its source states. */
interface ProviderTestObject {
  readonly file: string
  /** The directory whose `scenarios.ts` builds the `native` fixture. */
  readonly scenarioDirectory: string
  /** The workspace that the `native` fixture reads its workspace ID from. */
  readonly nativeWorkspace: string
  /** The member name of the `AgentProvider` that the `ProviderAgent` of that workspace opens. */
  readonly providerName: string
}

/**
 * Read each provider test object from its source.
 *
 * Importing a fixture file runs `base.extend` of Playwright outside a Playwright run, so the guard reads the source
 * and imports only the scenario module, which holds no fixture.
 */
function providerTestObjects(): ProviderTestObject[] {
  const files = readdirSync(import.meta.dirname).filter(name => name.endsWith('-fixtures.ts')).sort()
  const objects: ProviderTestObject[] = []
  for (const file of files) {
    const source = readFileSync(join(import.meta.dirname, file), 'utf-8')
    if (!source.includes('cliSkipFixture('))
      continue
    const scenarioImport = /^import \{ nativeContext \} from '\.\/([\w-]+)\/scenarios'$/m.exec(source)
    const nativeFixture = /^ {2}native: async \(\{ page, modelScript, leapmuxServer, (\w+) \}, use\) => \{\n {4}await use\(await nativeContext\(\{ page, modelScript, leapmuxServer, workspaceId: (\w+)\.workspaceId \}\)\)\n {2}\},$/m.exec(source)
    if (!scenarioImport || !nativeFixture)
      throw new Error(`${file} builds no native fixture from the nativeContext of its provider directory`)
    if (nativeFixture[1] !== nativeFixture[2])
      throw new Error(`${file} asks for ${nativeFixture[1]} but reads the workspace ID of ${nativeFixture[2]}`)
    const workspace = new RegExp(`^ {2}${nativeFixture[1]}: authenticatedAgentWorkspace\\(\\s*(?:\\{\\s*\\.\\.\\.)?(\\w+)\\b`, 'm').exec(source)
    if (!workspace)
      throw new Error(`${file} does not open ${nativeFixture[1]} through authenticatedAgentWorkspace`)
    const agent = new RegExp(`^export const ${workspace[1]}: ProviderAgent = \\{ provider: AgentProvider\\.(\\w+),`, 'm').exec(source)
    if (!agent)
      throw new Error(`${file} declares no ProviderAgent named ${workspace[1]}`)
    objects.push({ file, scenarioDirectory: scenarioImport[1]!, nativeWorkspace: nativeFixture[1]!, providerName: agent[1]! })
  }
  return objects
}

/**
 * Fixtures that hold no member: a context that calls one of them fails, and a context that copies them holds the same
 * objects.
 */
const UNIT_FIXTURES = {
  page: Object.freeze({}),
  modelScript: Object.freeze({}),
  leapmuxServer: Object.freeze({}),
  workspaceId: 'unit-workspace',
} as unknown as NativeContextFixtures

describe('provider test objects', () => {
  const objects = providerTestObjects()

  it('gives every provider exactly one test object', () => {
    const providers = Object.values(AgentProvider).filter((value): value is AgentProvider => typeof value === 'number' && value !== AgentProvider.UNSPECIFIED)
    expect(objects.map(object => AgentProvider[object.providerName as keyof typeof AgentProvider]).sort((a, b) => a - b)).toEqual(providers.sort((a, b) => a - b))
  })

  it('builds each native fixture from its main authenticated workspace', () => {
    for (const object of objects)
      expect(object.nativeWorkspace, object.file).toMatch(/^authenticated\w+Workspace$/)
  })

  it('builds each native fixture through the nativeContext of the provider that its workspace opens', async () => {
    for (const object of objects) {
      const scenarios = await import(`./${object.scenarioDirectory}/scenarios.ts`) as { nativeContext: (fixtures: NativeContextFixtures) => Promise<ManagedNativeScenarioContext> }
      const context = await scenarios.nativeContext(UNIT_FIXTURES)
      expect(AgentProvider[context.provider], object.file).toBe(object.providerName)
      expect(context.page, object.file).toBe(UNIT_FIXTURES.page)
      expect(context.modelScript, object.file).toBe(UNIT_FIXTURES.modelScript)
      expect(context.leapmuxServer, object.file).toBe(UNIT_FIXTURES.leapmuxServer)
      expect(context.workspaceId, object.file).toBe(UNIT_FIXTURES.workspaceId)
    }
  })
})
