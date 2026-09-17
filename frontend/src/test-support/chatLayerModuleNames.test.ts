import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'
import { importedNames } from '~/test-support/typescriptImports'
// The file-NAMING guard for the three-layer chat pipeline. `providerLayering.test.ts`
// and `irLayering.test.ts` say what a layer may import and draw; this says what its
// modules are called.
//
// Two conventions used to run side by side in `results/` with nothing to tell them
// apart: `ReadResultView.tsx` beside `readFileResult.tsx`, both component-only modules
// about the same tool kind. A reader could not predict either name, and the answer to
// "where does my new file go?" was whichever neighbour they happened to open.
//
// The classifier rule says where one JOB lives. `providers/README.md` gives
// `plugin.ts` the `registerProvider` call and nothing else another module can hold,
// and it gives `classification.ts` the answer to "which shared message category one
// frame takes".
// A classifier runs 150 to 330 lines of a provider's own vocabulary, so a plugin that
// holds one is more classifier than registration. The rule reads the `classify` hook
// of each provider object and requires that the hook arrive from a module named
// `classification`.
//
// WHAT THE CLASSIFIER RULE DOES NOT CATCH:
//
//   - A SUB-HOOK. `classifyToolCallUpdate` answers one frame's category too, and the
//     ACP family entry takes one as an option. Goose declares its own beside its
//     registration. This rule reads the `classify` property alone.
//   - Everything else in `plugin.ts`. The rule states where the classifier lives. It
//     does not state that the file holds the registration and nothing more.
//   - A re-export. The rule reads the module specifier of the import, so a
//     `classification.ts` symbol that reaches the plugin through a third module reports
//     that third module's name.
//   - A provider object that no object literal states. A plugin assembled by
//     `Object.assign` or by a spread at run time carries no `classify` property for
//     this walk to read.

const CHAT_DIR = join(frontendRoot, 'src/components/chat')
const PROVIDERS_DIR = join(CHAT_DIR, 'providers')

/**
 * The directories the three-layer pipeline occupies.
 *
 * Scoped rather than the whole of `components/chat/`, because the rule below is TRUE
 * here and not everywhere: `controls/` holds modules named for the control they serve
 * (`ExitPlanModeControl.tsx` exports `ExitPlanModeContent`), and that convention is its
 * own. Widening this guard would either fail on eleven files or need an allow-list
 * long enough to stop meaning anything.
 */
const LAYER_DIRS = ['providers', 'ir', 'results', 'widgets']

function layerModules(extension: '.ts' | '.tsx'): string[] {
  return LAYER_DIRS.flatMap(dir => collectFiles(join(CHAT_DIR, dir), {
    matches: name => name.endsWith(extension)
      && !name.endsWith('.test.ts')
      && !name.endsWith('.test.tsx')
      && !name.endsWith('.css.ts')
      && !name.endsWith('.fixtures.ts'),
  }))
}

/** Whether the module exports a `const` or `function` with exactly this name. */
function exportsSymbol(source: string, name: string): boolean {
  return new RegExp(`^export (?:const|function) ${name}\\b`, 'm').test(source)
}

/** Every provider module that ships, in either extension. */
function providerModules(): string[] {
  return collectFiles(PROVIDERS_DIR, {
    matches: name => (name.endsWith('.ts') || name.endsWith('.tsx'))
      && !name.endsWith('.test.ts')
      && !name.endsWith('.test.tsx')
      && !name.endsWith('.fixtures.ts'),
  })
}

/** One `classify` hook of one provider object. */
interface ClassifyHook {
  /** The line the property sits on, counted from 1. */
  line: number
  /**
   * The module the hook arrives from.
   *
   * `null` says that the file itself holds the body: an inline method, an inline
   * function, or an identifier that the same file declares.
   */
  module: string | null
}

/**
 * The identifier a hook expression resolves to.
 *
 * Three forms reach one: the reference (`classifyPiMessage`), the factory call that
 * the ACP family entry makes (`classifyACPMessage({...})`), and either one inside
 * parentheses or behind an `as`. An inline function resolves to no identifier, which
 * is what marks it as the file's own.
 */
function rootIdentifier(node: ts.Expression): string | undefined {
  let current = node
  for (;;) {
    if (ts.isParenthesizedExpression(current) || ts.isAsExpression(current)) {
      current = current.expression
      continue
    }
    if (ts.isCallExpression(current)) {
      current = current.expression
      continue
    }
    return ts.isIdentifier(current) ? current.text : undefined
  }
}

/**
 * Every `classify` hook one module states, with the module each one arrives from.
 *
 * A PARSE rather than a text scan, for the reason `toolTableEntriesAreAnnotated`
 * gives: the rule turns on the KIND of the property (a method, a function, a
 * reference) and on the import that the name resolves to. It also reads no comment,
 * because a comment is not a node.
 */
function classifyHooks(fileName: string, source: string): ClassifyHook[] {
  const file = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true)
  // The shared syntax-tree collector states every name an import introduces, so the
  // rule sees each form the grammar holds rather than the ones this test learned.
  const imported = new Map(importedNames(source, fileName).map(entry => [entry.name, entry.specifier]))
  const hooks: ClassifyHook[] = []

  function hookModule(property: ts.ObjectLiteralElementLike): string | null {
    if (ts.isMethodDeclaration(property))
      return null
    const value = ts.isPropertyAssignment(property)
      ? property.initializer
      : ts.isShorthandPropertyAssignment(property) ? property.name : undefined
    const root = value === undefined ? undefined : rootIdentifier(value)
    return root === undefined ? null : imported.get(root) ?? null
  }

  function visit(node: ts.Node): void {
    if (ts.isObjectLiteralExpression(node)) {
      for (const property of node.properties) {
        if (property.name === undefined || !ts.isIdentifier(property.name) || property.name.text !== 'classify')
          continue
        hooks.push({
          line: file.getLineAndCharacterOfPosition(property.getStart(file)).line + 1,
          module: hookModule(property),
        })
      }
    }
    ts.forEachChild(node, visit)
  }

  // The tree walk needs no import pass of its own: the names the hook resolution
  // reads were collected from the whole file before it starts, whatever the order.
  ts.forEachChild(file, visit)
  return hooks
}

/** Whether a module specifier points at a `classification` module. */
function isClassificationModule(specifier: string): boolean {
  return specifier.endsWith('/classification')
}

/**
 * The provider that registers a classifier, and the module it takes one from.
 *
 * Discovery is the walk and not this table, so a provider a later change adds is
 * covered with no edit here. The table exists for the opposite failure: a matcher that
 * stops reading a hook reports nothing, which looks exactly like a clean tree. A name
 * here that the walk no longer finds fails the case below and states which provider
 * went unread.
 */
const PROVIDER_CLASSIFIERS: Readonly<Record<string, string | null>> = {
  'acp/registerACPProvider.ts': './classification',
  'claude/plugin.ts': './classification',
  'codex/plugin.ts': './classification',
  'copilot/plugin.ts': './classification',
  'pi/plugin.ts': './classification',
  'zcode/plugin.ts': './classification',
}

describe('the chat render layers name their modules one way', () => {
  it('finds the modules it guards', () => {
    expect(layerModules('.tsx').length).toBeGreaterThan(40)
  })

  /**
   * A PascalCase `.tsx` module is ONE component, and carries that component's name.
   *
   * The inverse is what the layers use for everything else: a camelCase module is named
   * after the IR shape it reads or draws, so `ir/searchResult.ts` and
   * `results/searchResult.tsx` pair by sight. Mixing the two hid that pairing -- and a
   * PascalCase name that did NOT match its export would hide it twice, because the file
   * then claims to be a component nobody can import under that name.
   */
  it('gives a PascalCase component module the name of its component', () => {
    const offences: string[] = []
    for (const file of layerModules('.tsx')) {
      const name = basename(file, '.tsx')
      if (!/^[A-Z]/.test(name))
        continue
      if (!exportsSymbol(readFileSync(file, 'utf8'), name))
        offences.push(`${posixRelative(CHAT_DIR, file)} exports no component named ${name}`)
    }
    expect(
      offences,
      'A PascalCase module in these directories is one component and carries its name. '
      + 'Rename the file to the component it exports, or give the module a camelCase name '
      + 'matching the IR shape it draws.',
    ).toEqual([])
  })

  /**
   * No provider keeps a `renderers/` directory.
   *
   * Layer 1 returns IR and never draws -- `providerLayering.test.ts` enforces that on
   * the JSX itself. Four providers still carried a `renderers/` directory from before
   * the pipeline closed, holding modules that answer `DividerIR` and
   * `NotificationEntryIR`. Those read the provider's own bytes, so they are extraction,
   * and the name sent every reader to the wrong layer.
   */
  it('keeps no renderers directory under a provider', () => {
    const providers = join(CHAT_DIR, 'providers')
    const offences = readdirSync(providers, { withFileTypes: true })
      .filter(entry => entry.isDirectory() && existsSync(join(providers, entry.name, 'renderers')))
      .map(entry => `providers/${entry.name}/renderers`)
    expect(
      offences,
      'A module that reads the provider\'s bytes into IR belongs in `extractors/`. '
      + 'Nothing in the provider layer draws.',
    ).toEqual([])
  })

  /**
   * A provider's classifier lives in `classification.ts`.
   *
   * The five cases below pin the matcher on sources of their own, because a matcher
   * that reads no hook reports no offence and passes.
   */
  it('reads an inline classify method as the file\'s own', () => {
    const source = `const plugin = {\n  classify(input) {\n    return { kind: 'unknown' }\n  },\n}\n`
    expect(classifyHooks('plugin.ts', source)).toStrictEqual([{ line: 2, module: null }])
  })

  it('reads an inline arrow as the file\'s own', () => {
    const source = `const plugin = {\n  classify: input => ({ kind: 'unknown' }),\n}\n`
    expect(classifyHooks('plugin.ts', source)).toStrictEqual([{ line: 2, module: null }])
  })

  it('reads a classifier the same file declares as the file\'s own', () => {
    const source = `function classifyThing(input) {\n  return { kind: 'unknown' }\n}\n\nconst plugin = {\n  classify: classifyThing,\n}\n`
    expect(classifyHooks('plugin.ts', source)).toStrictEqual([{ line: 6, module: null }])
  })

  it('reads an imported classifier as the module it arrives from', () => {
    const source = `import { classifyThing } from './classification'\n\nconst plugin = {\n  classify: classifyThing,\n}\n`
    expect(classifyHooks('plugin.ts', source)).toStrictEqual([{ line: 4, module: './classification' }])
  })

  it('reads a classifier factory call as the module it arrives from', () => {
    const source = `import { classifyACPMessage } from '../acp/classification'\n\nconst plugin = {\n  classify: classifyACPMessage({}),\n}\n`
    expect(classifyHooks('plugin.ts', source)).toStrictEqual([{ line: 4, module: '../acp/classification' }])
  })

  it('finds the classifier of every provider that registers one', () => {
    const found: Record<string, string | null> = {}
    for (const file of providerModules()) {
      for (const hook of classifyHooks(file, readFileSync(file, 'utf8')))
        found[posixRelative(PROVIDERS_DIR, file)] = hook.module
    }
    expect(found).toStrictEqual(PROVIDER_CLASSIFIERS)
  })

  /**
   * `plugin.ts` holds the `registerProvider` call, and the classifier is the largest
   * thing it can hand to another module.
   *
   * The hook must be a reference, and the reference must come from
   * `classification.ts`. A classifier written beside the registration reads as a
   * plugin that also parses, and the next provider copies the file it sees.
   */
  it('takes every provider classifier from a classification module', () => {
    const offences: string[] = []
    for (const file of providerModules()) {
      const relative = posixRelative(PROVIDERS_DIR, file)
      if (basename(file) === 'classification.ts')
        continue
      for (const hook of classifyHooks(file, readFileSync(file, 'utf8'))) {
        if (hook.module === null)
          offences.push(`${relative}:${hook.line} states its own classify hook`)
        else if (!isClassificationModule(hook.module))
          offences.push(`${relative}:${hook.line} takes classify from ${hook.module}`)
      }
    }
    expect(
      offences,
      'The `classify` hook belongs to `classification.ts`, which `providers/README.md` '
      + 'gives the answer to "which shared message category one frame takes". Move the '
      + 'classifier there and import it here.',
    ).toStrictEqual([])
  })
})
