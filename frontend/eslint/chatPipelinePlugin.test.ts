import type { ProviderFrameKind } from '../src/generated/contracts/provider-frame-kinds'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { TSESLint } from '@typescript-eslint/utils'
import { describe, expect, it } from 'vitest'
import plugin, { rulesVersion } from './chatPipelinePlugin'

const MODULES = [
  ['chatPipelinePlugin.ts', 'rule code'],
  ['providerWireTokens.ts', 'token code'],
] as const
const KINDS: readonly ProviderFrameKind[] = [
  { literal: 'item.started', match: 'name', source: 'test-protocol events' },
]

// ESLint writes a plugin into its cache key as `meta.name@meta.version`. The version
// must change with each input of the rules, or `eslint --cache` keeps a stale result.
describe('rulesVersion', () => {
  it('gives the same version for the same rules and tokens', () => {
    expect(rulesVersion(MODULES, KINDS)).toBe(rulesVersion(MODULES, [...KINDS]))
  })

  it('changes when a contract gains a frame kind', () => {
    const more = [
      ...KINDS,
      { literal: 'item.completed', match: 'name', source: 'test-protocol events' } as const,
    ]
    expect(rulesVersion(MODULES, more)).not.toBe(rulesVersion(MODULES, KINDS))
  })

  it('changes when a frame kind moves to another table', () => {
    const moved = [
      { ...KINDS[0]!, source: 'other-protocol events' },
    ]
    expect(rulesVersion(MODULES, moved)).not.toBe(rulesVersion(MODULES, KINDS))
  })

  it('changes when a rule module changes', () => {
    const edited = [
      MODULES[0],
      ['providerWireTokens.ts', 'token code, edited'],
    ] as const
    expect(rulesVersion(edited, KINDS)).not.toBe(rulesVersion(MODULES, KINDS))
  })

  // The separator keeps the boundary between a name and its source, so text that
  // moves from one to the other is still a change.
  it('changes when text moves between a module name and its source', () => {
    expect(rulesVersion([
      ['ab', 'c'],
    ], KINDS)).not.toBe(rulesVersion([
      ['a', 'bc'],
    ], KINDS))
  })
})

describe('chatPipelinePlugin', () => {
  it('states a name and a version, so the eslint cache key includes the rules', () => {
    expect(plugin.meta?.name).toBe('chat-pipeline')
    expect(plugin.meta?.version).toMatch(/^[0-9a-f]{16}$/)
  })
})

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')

/** A module of the chat model, as the importing file. The rule reads its path alone. */
const MODEL_FILE = resolve(frontendRoot, 'src/components/chat/model/probe.ts')

/**
 * The rule IDs that `layer-imports` reports for one import statement in `filename`.
 *
 * The linter runs in this process with the plugin alone, not with `eslint.config.ts`, so
 * no other rule and no type information takes part. A value import parses without the
 * TypeScript parser, and the rule reads only the specifier.
 */
function layerImportReports(filename: string, specifier: string): Array<string | null> {
  const linter = new TSESLint.Linter({ configType: 'flat' })
  const messages = linter.verify(`import { probe } from '${specifier}'\nvoid probe\n`, [
    {
      files: ['**/*.ts'],
      plugins: { 'chat-pipeline': plugin },
      rules: { 'chat-pipeline/layer-imports': 'error' },
    },
  ], { filename })
  return messages.map(message => message.ruleId)
}

// The model may import three pure modules of `../diff/` and no other. The rule matches
// the module path after it resolves the specifier, so each spelling of an allowed module
// passes, and a path that only resembles one fails.
describe('chat-pipeline/layer-imports', () => {
  it.each([
    '../diff/diffBuilder',
    '../diff/diffTypes',
    '../diff/unifiedDiffParser',
    '../diff/diffBuilder.ts',
    '~/components/chat/diff/unifiedDiffParser',
    './toolCall',
  ])('lets the model import %s', (specifier) => {
    expect(layerImportReports(MODEL_FILE, specifier)).toEqual([])
  })

  it.each([
    // The barrel re-exports `DiffViewer`, which draws.
    '../diff',
    '../diff/index',
    '../diff/DiffViewer',
    '../diff/diffWorkerClient',
    // A name that only starts with the name of an allowed module.
    '../diff/diffBuilderCache',
    // An allowed name in another directory.
    '../diff/legacy/diffBuilder',
    '~/components/common/diffTypes',
  ])('refuses a model import of %s', (specifier) => {
    expect(layerImportReports(MODEL_FILE, specifier)).toEqual(['chat-pipeline/layer-imports'])
  })
})
