import type { ManagedNativeScenarioContext, NativeContextFixtures } from './helpers/nativeScenario'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import ts from 'typescript'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base } from './fixtures'
import { createTranscriptGuard, runTranscriptGuard } from './helpers/transcriptGuard'
import * as workspaceHelpers from './helpers/workspace'
import { processTest } from './process-control-fixtures'
import { cliSkipFixture, createProviderProcessTest, createProviderTest } from './provider-fixture-factory'

// Use real Playwright registration without the shared browser lifecycle. Browser specs exercise that lifecycle.
vi.mock('./fixtures', async () => {
  const { test } = await import('@playwright/test')
  return { test }
})

vi.mock('./helpers/transcriptGuard', async importOriginal => ({
  ...await importOriginal<typeof import('./helpers/transcriptGuard')>(),
  runTranscriptGuard: vi.fn(async () => {}),
}))

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
  /** The `ProviderAgent` that opens that workspace. The scenario module of the provider directory exports it. */
  readonly agentName: string
  /** The member name of the `AgentProvider` that the `ProviderAgent` of that workspace opens. */
  readonly providerName: string
  readonly fixtures: RegisteredProviderFixtures
}

function providerAgentMember(source: string, agentName: string): string | null {
  const file = ts.createSourceFile('provider-agent.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const matches: Array<{ statement: ts.VariableStatement, declaration: ts.VariableDeclaration }> = []
  for (const statement of file.statements) {
    if (!ts.isVariableStatement(statement))
      continue
    for (const declaration of statement.declarationList.declarations) {
      if (ts.isIdentifier(declaration.name) && declaration.name.text === agentName)
        matches.push({ statement, declaration })
    }
  }
  const match = matches[0]
  if (matches.length !== 1 || !match)
    return null
  const { statement, declaration } = match
  if (!(statement.declarationList.flags & ts.NodeFlags.Const) || !statement.modifiers?.some(modifier => modifier.kind === ts.SyntaxKind.ExportKeyword))
    return null
  const annotation = declaration.type
  if (!annotation || !ts.isTypeReferenceNode(annotation) || !ts.isIdentifier(annotation.typeName) || annotation.typeName.text !== 'ProviderAgent' || annotation.typeArguments?.length)
    return null
  const initializer = declaration.initializer
  if (!initializer || !ts.isObjectLiteralExpression(initializer))
    return null
  if (initializer.properties.some(property => ts.isSpreadAssignment(property) || (property.name && ts.isComputedPropertyName(property.name))))
    return null
  const providers = initializer.properties.filter(property => property.name && (ts.isIdentifier(property.name) || ts.isStringLiteral(property.name)) && property.name.text === 'provider')
  const provider = providers[0]
  if (providers.length !== 1 || !provider || !ts.isPropertyAssignment(provider))
    return null
  const value = provider.initializer
  if (!ts.isPropertyAccessExpression(value) || value.questionDotToken || !ts.isIdentifier(value.expression) || value.expression.text !== 'AgentProvider' || !ts.isIdentifier(value.name))
    return null
  return value.name.text
}

interface RegisteredProviderFixtures {
  native: (args: object, use: (context: ManagedNativeScenarioContext) => Promise<void>) => Promise<void>
  modelScript: (args: object, use: (script: unknown) => Promise<void>) => Promise<void>
  providerWorkspace: unknown
  [key: string]: unknown
}

/** Import each provider and inspect the fixtures that it registers with Playwright. */
async function providerTestObjects(): Promise<ProviderTestObject[]> {
  const files = readdirSync(import.meta.dirname).filter(name => name.endsWith('-fixtures.ts')).sort()
  const objects: ProviderTestObject[] = []
  const capture = vi.spyOn(base, 'extend')
  try {
    for (const file of files) {
      const source = readFileSync(join(import.meta.dirname, file), 'utf-8')
      if (!source.includes('createProviderTest('))
        continue
      const scenarioImport = /^import \{ ([\w, ]+) \} from '\.\/([\w-]+)\/scenarios'$/m.exec(source)
      const scenarioNames = scenarioImport?.[1]?.split(', ') ?? []
      const agentName = scenarioNames.find(name => name.endsWith('_AGENT'))
      if (!scenarioImport || !scenarioNames.includes('nativeContext') || !agentName)
        throw new Error(`${file} must import its canonical agent and native context from its scenario module.`)
      if (/^export const \w+: ProviderAgent = /m.test(source))
        throw new Error(`${file} declares a ProviderAgent, which the scenario module of its provider directory holds`)
      const scenarioDirectory = scenarioImport[2]!
      const scenarioSource = readFileSync(join(import.meta.dirname, scenarioDirectory, 'scenarios.ts'), 'utf-8')
      const providerName = providerAgentMember(scenarioSource, agentName)
      if (!providerName)
        throw new Error(`${scenarioDirectory}/scenarios.ts must declare ${agentName} as a ProviderAgent.`)
      capture.mockClear()
      const providerFile = file.slice(0, -'-fixtures.ts'.length)
      await import(`./${providerFile}-fixtures.ts`)
      expect(capture, file).toHaveBeenCalledTimes(1)
      const fixtures = capture.mock.calls[0]![0] as unknown as RegisteredProviderFixtures
      const aliases = Object.keys(fixtures).filter(name => /^authenticated\w+Workspace$/.test(name))
      expect(aliases, file).toHaveLength(1)
      objects.push({ file, scenarioDirectory, nativeWorkspace: aliases[0]!, agentName, providerName, fixtures })
    }
  }
  finally {
    capture.mockRestore()
  }
  return objects
}

/**
 * These fixtures have no members. A context that calls a member fails.
 * A context that copies these fixtures retains the same objects.
 */
const UNIT_FIXTURES = {
  page: Object.freeze({}),
  modelScript: Object.freeze({}),
  leapmuxServer: Object.freeze({}),
  workspaceId: 'unit-workspace',
} as unknown as NativeContextFixtures

/** The members of a scenario module that the checks read: its `nativeContext`, and its `ProviderAgent` by name. */
type ScenarioModule = Record<string, unknown> & { nativeContext: (fixtures: NativeContextFixtures) => Promise<ManagedNativeScenarioContext> }

describe('provider test objects', () => {
  let objects: ProviderTestObject[] = []
  /** The scenario module of each test object, in the order of `objects`. */
  let scenarioModules: ScenarioModule[] = []

  // Import each fixture once, then read the canonical agents from the scenario modules.
  beforeAll(async () => {
    objects = await providerTestObjects()
    scenarioModules = await Promise.all(objects.map(async object => await import(`./${object.scenarioDirectory}/scenarios.ts`) as ScenarioModule))
  })

  it('gives every provider exactly one test object', () => {
    const providers = Object.values(AgentProvider).filter((value): value is AgentProvider => typeof value === 'number' && value !== AgentProvider.UNSPECIFIED)
    expect(objects.map(object => AgentProvider[object.providerName as keyof typeof AgentProvider]).sort((a, b) => a - b)).toEqual(providers.sort((a, b) => a - b))
  })

  it('builds each native fixture from its main authenticated workspace', () => {
    for (const object of objects) {
      expect(object.nativeWorkspace, object.file).toMatch(/^authenticated\w+Workspace$/)
      expect(object.fixtures.providerWorkspace, object.file).toBeTypeOf('function')
    }
  })

  it('builds each native fixture through the nativeContext of the provider that its workspace opens', async () => {
    expect(scenarioModules).toHaveLength(objects.length)
    for (const [index, object] of objects.entries()) {
      const scenarios = scenarioModules[index]!
      const providerWorkspace = { workspaceId: UNIT_FIXTURES.workspaceId }
      const alias = object.fixtures[object.nativeWorkspace] as (args: object, use: (workspace: unknown) => Promise<void>) => Promise<void>
      const useWorkspace = vi.fn(async () => {})
      await alias({ providerWorkspace }, useWorkspace)
      expect(useWorkspace, object.file).toHaveBeenCalledExactlyOnceWith(providerWorkspace)
      const useNative = vi.fn(async (_context: ManagedNativeScenarioContext) => {})
      await object.fixtures.native({ ...UNIT_FIXTURES, providerWorkspace }, useNative)
      expect(useNative, object.file).toHaveBeenCalledTimes(1)
      const context = useNative.mock.calls[0]![0]
      expect(AgentProvider[context.provider], object.file).toBe(object.providerName)
      // A helper that opens a new native agent creates its working directory by the rule of this agent.
      expect(context.providerAgent, object.file).toBe(scenarios[object.agentName])
      expect(context.providerAgent.provider, object.file).toBe(context.provider)
      expect(context.page, object.file).toBe(UNIT_FIXTURES.page)
      expect(context.modelScript, object.file).toBe(UNIT_FIXTURES.modelScript)
      expect(context.leapmuxServer, object.file).toBe(UNIT_FIXTURES.leapmuxServer)
      expect(context.workspaceId, object.file).toBe(UNIT_FIXTURES.workspaceId)
    }
  })
})

describe('providerAgentMember', () => {
  it('reads the current inline declaration', () => {
    expect(providerAgentMember('export const SAMPLE_AGENT: ProviderAgent = { provider: AgentProvider.CODEX, prefix: \'sample\' }', 'SAMPLE_AGENT')).toBe('CODEX')
  })

  it('reads the actual multiline Muse declaration', () => {
    const source = readFileSync(join(import.meta.dirname, 'muse-code/scenarios.ts'), 'utf8')
    expect(providerAgentMember(source, 'MUSE_AGENT')).toBe('MUSE_CODE')
  })

  it('reads comments and whitespace around the initializer and provider', () => {
    const source = 'export const SAMPLE_AGENT: ProviderAgent = /* initializer */ {\n  /* owner */ provider: /* enum */ AgentProvider.CODEX,\n  prefix: \'sample\',\n}'
    expect(providerAgentMember(source, 'SAMPLE_AGENT')).toBe('CODEX')
  })

  it('reads the provider after an unrelated property', () => {
    expect(providerAgentMember('export const SAMPLE_AGENT: ProviderAgent = { prefix: \'sample\', provider: AgentProvider.CODEX }', 'SAMPLE_AGENT')).toBe('CODEX')
  })

  it.each([
    '',
    'export const OTHER_AGENT: ProviderAgent = { provider: AgentProvider.CODEX, prefix: \'sample\' }',
    'const SAMPLE_AGENT: ProviderAgent = { provider: AgentProvider.CODEX, prefix: \'sample\' }',
    'export const SAMPLE_AGENT: OtherAgent = { provider: AgentProvider.CODEX, prefix: \'sample\' }',
    'export const SAMPLE_AGENT: ProviderAgent = { provider: 1, prefix: \'sample\' }',
    'export const SAMPLE_AGENT: ProviderAgent = { provider: OtherProvider.CODEX, prefix: \'sample\' }',
    'export const SAMPLE_AGENT: ProviderAgent = { prefix: \'sample\' }',
    'export const SAMPLE_AGENT: ProviderAgent = { provider: AgentProvider.CODEX, provider: AgentProvider.PI }',
    'export const SAMPLE_AGENT: ProviderAgent = { provider: AgentProvider.CODEX, ...anotherAgent }',
    'export let SAMPLE_AGENT: ProviderAgent = { provider: AgentProvider.CODEX }',
    'export const SAMPLE_AGENT: ProviderAgent = anotherAgent',
    'export const SAMPLE_AGENT: ProviderAgent = { provider: AgentProvider.CODEX, [key]: value }',
    'export const SAMPLE_AGENT: ProviderAgent = { provider: AgentProvider.CODEX }; const SAMPLE_AGENT: ProviderAgent = { provider: AgentProvider.PI }',
    'export const SAMPLE_AGENT: ProviderAgent = { provider: AgentProvider?.CODEX }',
  ])('rejects an absent or ambiguous exact declaration: %s', (source) => {
    expect(providerAgentMember(source, 'SAMPLE_AGENT')).toBeNull()
  })
})

describe('createProviderTest', () => {
  const agent = { provider: AgentProvider.CODEX, prefix: 'standard-unit' }
  type Options = Parameters<typeof createProviderTest<'authenticatedUnitWorkspace'>>[0]

  function register(overrides: Partial<Options> = {}): RegisteredProviderFixtures {
    const capture = vi.spyOn(base, 'extend')
    try {
      createProviderTest({
        agent,
        nativeContext: async fixtures => ({ ...fixtures, provider: agent.provider, providerAgent: agent }) as ManagedNativeScenarioContext,
        skipReason: null,
        workspaceName: 'authenticatedUnitWorkspace',
        ...overrides,
      })
      expect(capture).toHaveBeenCalledTimes(1)
      return capture.mock.calls[0]![0] as unknown as RegisteredProviderFixtures
    }
    finally {
      capture.mockRestore()
    }
  }

  it('passes workspace options and diagnostics without replacing the canonical provider', () => {
    const onFailure = vi.fn(async () => {})
    const openOptions = { optionValues: { permissionMode: 'default' } }
    const capture = vi.spyOn(workspaceHelpers, 'authenticatedAgentWorkspace')
    try {
      register({ workspaceOptions: { openOptions, onFailure } })
      expect(capture).toHaveBeenCalledExactlyOnceWith({ ...agent, openOptions, onFailure })
    }
    finally {
      capture.mockRestore()
    }
  })

  it.each(['original-workspace', 'overridden-workspace'])('uses the same workspace for the alias and native context: %s', async (workspaceId) => {
    const fixtures = register()
    const providerWorkspace = { workspaceId, workingDir: { path: '/unit/directory' } }
    const alias = fixtures.authenticatedUnitWorkspace as (args: object, use: (workspace: unknown) => Promise<void>) => Promise<void>
    const useAlias = vi.fn(async () => {})
    await alias({ providerWorkspace }, useAlias)
    expect(useAlias).toHaveBeenCalledExactlyOnceWith(providerWorkspace)
    const useNative = vi.fn(async (_context: ManagedNativeScenarioContext) => {})
    await fixtures.native({ ...UNIT_FIXTURES, providerWorkspace }, useNative)
    expect(useNative).toHaveBeenCalledExactlyOnceWith({ ...UNIT_FIXTURES, workspaceId, provider: agent.provider, providerAgent: agent })
  })

  it.each(['provider', 'providerAgent'] as const)('rejects a mismatched %s before use', async (field) => {
    const fixtures = register({
      nativeContext: async input => ({
        ...input,
        provider: agent.provider,
        providerAgent: agent,
        [field]: field === 'provider' ? AgentProvider.CLAUDE_CODE : { ...agent },
      }) as ManagedNativeScenarioContext,
    })
    const use = vi.fn(async () => {})
    await expect(fixtures.native({ ...UNIT_FIXTURES, providerWorkspace: { workspaceId: 'unit-workspace' } }, use)).rejects.toThrow('must use the provider')
    expect(use).not.toHaveBeenCalled()
  })

  it.each(['workspaceId', 'page', 'modelScript', 'leapmuxServer'] as const)('rejects a context that replaces %s', async (field) => {
    const fixtures = register({
      nativeContext: async input => ({ ...input, provider: agent.provider, providerAgent: agent, [field]: field === 'workspaceId' ? 'other-workspace' : {} }) as ManagedNativeScenarioContext,
    })
    const use = vi.fn(async () => {})
    await expect(fixtures.native({ ...UNIT_FIXTURES, providerWorkspace: { workspaceId: 'unit-workspace' } }, use)).rejects.toThrow('must retain its workspace')
    expect(use).not.toHaveBeenCalled()
  })

  it('propagates context construction failures without calling use', async () => {
    const failure = new Error('Native context construction failed.')
    const fixtures = register({
      nativeContext: async () => {
        throw failure
      },
    })
    const use = vi.fn(async () => {})
    await expect(fixtures.native({ ...UNIT_FIXTURES, providerWorkspace: { workspaceId: 'unit-workspace' } }, use)).rejects.toBe(failure)
    expect(use).not.toHaveBeenCalled()
  })

  it('propagates a failure from the test body', async () => {
    const fixtures = register()
    const failure = new Error('The test body failed.')
    await expect(fixtures.native({ ...UNIT_FIXTURES, providerWorkspace: { workspaceId: 'unit-workspace' } }, async () => {
      throw failure
    })).rejects.toBe(failure)
  })

  it.each([
    { modelRules: [] },
    { modelRules: [{ name: 'title', when: { system: 'title helper' }, respond: { text: 'A title' } }] },
  ])('registers housekeeping rules before use: %j', async ({ modelRules }) => {
    const events: string[] = []
    const fixtures = register({ modelRules })
    const script = { rule: vi.fn(async () => {
      events.push('rules')
    }) }
    await fixtures.modelScript({ modelScript: script }, async (actual) => {
      expect(actual).toBe(script)
      events.push('use')
    })
    expect(events).toEqual(modelRules.length === 0 ? ['use'] : ['rules', 'use'])
    expect(script.rule.mock.calls).toEqual(modelRules.length === 0 ? [] : [modelRules])
  })

  it('retains the CLI skip reason in the automatic fixture', async () => {
    const fixtures = register({ skipReason: 'The test CLI is absent.' })
    const [skip, options] = fixtures.cliSkip as ReturnType<typeof cliSkipFixture>
    const stop = new Error('Skipped.')
    const testInfo = { skip: vi.fn(() => {
      throw stop
    }) }
    const use = vi.fn(async () => {})
    await expect((skip as unknown as (args: object, use: () => Promise<void>, info: typeof testInfo) => Promise<void>)({}, use, testInfo)).rejects.toBe(stop)
    expect(options).toEqual({ auto: true })
    expect(testInfo.skip).toHaveBeenCalledExactlyOnceWith(true, 'The test CLI is absent.')
    expect(use).not.toHaveBeenCalled()
  })
})

describe('createProviderProcessTest', () => {
  it('checks the transcript after the process workspace fixture runs', async () => {
    const capture = vi.spyOn(processTest, 'extend')
    try {
      createProviderProcessTest({
        agent: { provider: AgentProvider.CODEX, prefix: 'process-unit' },
        nativeContext: async () => UNIT_FIXTURES as ManagedNativeScenarioContext,
        skipReason: null,
      })
      const registered = capture.mock.calls[0]?.[0] as unknown as {
        authenticatedWorkspace: (args: object, use: (workspace: unknown) => Promise<void>, info: object) => Promise<void>
      }
      const transcriptGuard = createTranscriptGuard()
      const authenticatedWorkspace = { workspaceId: 'workspace' }
      const info = { status: 'passed', expectedStatus: 'passed' }
      vi.mocked(runTranscriptGuard).mockClear()
      await registered.authenticatedWorkspace({ page: UNIT_FIXTURES.page, authenticatedWorkspace, transcriptGuard }, async (actual) => {
        expect(actual).toBe(authenticatedWorkspace)
        expect(runTranscriptGuard).not.toHaveBeenCalled()
      }, info)
      expect(runTranscriptGuard).toHaveBeenCalledExactlyOnceWith(UNIT_FIXTURES.page, transcriptGuard, info)
    }
    finally {
      capture.mockRestore()
    }
  })

  it('registers provider housekeeping before the process scenario uses its model script', async () => {
    const events: string[] = []
    const modelRules = [{ name: 'provider-title', when: { system: 'title helper' }, respond: { text: 'A title' } }]
    const options = {
      agent: { provider: AgentProvider.CODEX, prefix: 'process-unit' },
      nativeContext: async () => UNIT_FIXTURES as ManagedNativeScenarioContext,
      skipReason: null,
      modelRules,
    }
    const capture = vi.spyOn(processTest, 'extend')
    try {
      createProviderProcessTest(options)
      const registered = capture.mock.calls[0]?.[0] as unknown as {
        modelScript?: (args: object, use: (script: unknown) => Promise<void>) => Promise<void>
      }
      expect(registered.modelScript).toBeTypeOf('function')
      if (!registered.modelScript)
        throw new Error('The process fixture omitted its model rules.')
      const script = { rule: vi.fn(async () => {
        events.push('rules')
      }) }
      await registered.modelScript({ modelScript: script }, async (actual) => {
        expect(actual).toBe(script)
        events.push('use')
      })
      expect(script.rule).toHaveBeenCalledExactlyOnceWith(...modelRules)
      expect(events).toEqual(['rules', 'use'])
    }
    finally {
      capture.mockRestore()
    }
  })

  it('constructs the native context from the separate Worker and the authenticated provider workspace', async () => {
    const agent = { provider: AgentProvider.CODEX, prefix: 'process-unit' }
    const expected = { ...UNIT_FIXTURES, provider: agent.provider, providerAgent: agent } as ManagedNativeScenarioContext
    const nativeContext = vi.fn(async () => expected)
    const capture = vi.spyOn(processTest, 'extend')
    try {
      createProviderProcessTest({ agent, nativeContext, skipReason: null })
      const registered = capture.mock.calls[0]?.[0] as unknown as {
        native: (args: object, use: (context: ManagedNativeScenarioContext) => Promise<void>) => Promise<void>
      }
      const separateHubWorker = { hubUrl: 'http://process.invalid', workerId: 'process-worker', adminToken: 'process-token' }
      const use = vi.fn(async () => {})
      await registered.native({
        page: UNIT_FIXTURES.page,
        modelScript: UNIT_FIXTURES.modelScript,
        separateHubWorker,
        authenticatedWorkspace: { workspaceId: 'provider-workspace' },
      }, use)
      expect(nativeContext).toHaveBeenCalledExactlyOnceWith({
        page: UNIT_FIXTURES.page,
        modelScript: UNIT_FIXTURES.modelScript,
        leapmuxServer: separateHubWorker,
        workspaceId: 'provider-workspace',
      })
      expect(use).toHaveBeenCalledExactlyOnceWith(expected)
    }
    finally {
      capture.mockRestore()
    }
  })

  it('rejects a context from another provider before it runs the test', async () => {
    const agent = { provider: AgentProvider.CODEX, prefix: 'process-unit' }
    const other = { provider: AgentProvider.CLAUDE_CODE, prefix: 'other-unit' }
    const capture = vi.spyOn(processTest, 'extend')
    try {
      createProviderProcessTest({
        agent,
        nativeContext: async () => ({ ...UNIT_FIXTURES, provider: other.provider, providerAgent: other }) as ManagedNativeScenarioContext,
        skipReason: null,
      })
      const registered = capture.mock.calls[0]?.[0] as unknown as {
        native: (args: object, use: () => Promise<void>) => Promise<void>
      }
      const use = vi.fn(async () => {})
      await expect(registered.native({
        page: UNIT_FIXTURES.page,
        modelScript: UNIT_FIXTURES.modelScript,
        separateHubWorker: {},
        authenticatedWorkspace: { workspaceId: 'provider-workspace' },
      }, use)).rejects.toThrow('must use the provider')
      expect(use).not.toHaveBeenCalled()
    }
    finally {
      capture.mockRestore()
    }
  })
})
