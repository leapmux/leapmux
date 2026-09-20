import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { TOOL_KINDS } from '~/components/chat/model/toolKind'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'
import { importedNames } from '~/test-support/typescriptImports'

// The STRUCTURE guard for the three-layer chat pipeline. `eslint.config.ts`
// states what a layer may import, draw and assert; this file states what its
// files ARE: which modules exist, what they are called, and that every
// exception the linter config carves out stays pinned to a path that exists.
//
// The rules here are the ones no type system and no AST selector can read:
// a directory's contents, a file's export set, the module an identifier
// arrives from, and the allowlists that would silently forgive whatever new
// file took a stale path.

const CHAT_DIR = join(frontendRoot, 'src/components/chat')
const PROVIDERS_DIR = join(CHAT_DIR, 'providers')
const MODEL_DIR = join(CHAT_DIR, 'model')

/**
 * The provider `.tsx` modules that MAY draw.
 *
 * Each is a control surface: a permission prompt, a question form, a plan
 * approval. Those read a provider's own request payload and answer it, which is
 * not a transcript row -- the row model does not describe them. `eslint.config.ts`
 * lifts the JSX ban for exactly these four paths.
 */
const DRAWING_ALLOWED = [
  'codex/CodexControlActions.tsx',
  'cursor/CursorControlActions.tsx',
  'pi/PiControlActions.tsx',
  'pi/PiPlanApprovalActions.tsx',
]

/**
 * The display surfaces that MAY identify one provider in shared code.
 *
 * `AgentProviderIcon` is the whole list: an icon is a per-provider asset, and no
 * shared shape can supply one. `eslint.config.ts` lifts the decision selectors
 * for this one file.
 */
const PROVIDER_COMPARISON_ALLOWED = [
  'components/common/AgentProviderIcon.tsx',
]

/** The model's own builder: the one module the assertion ban exempts. */
const MODEL_BUILDER = 'model/createToolCall.ts'

/** The resolved-content constructor: the one module the brand ban exempts. */
const RESOLVED_CONTENT_BUILDER = 'providers/registry.ts'

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

/**
 * The assertion types the ESLint selector refuses, by the name the selector reads.
 *
 * Each one re-pairs a kind with a payload, a request or a result; the renderers
 * read those fields with no guard, so the row throws rather than draws. The
 * builder's exemption is ONE assertion, not a standing permission for the file:
 * a second one there is a new pairing nobody checked, and it would hide behind
 * the first.
 */
const FORBIDDEN_ASSERTION_TYPES = [
  'ToolCall',
  'ToolCallSpec',
  'ToolCallSpecVariant',
  'ToolCallVariant',
  'ToolRequestByKind',
  'ToolResultByKind',
  'ToolResult',
  'ParsedCall',
  'ResolvedCall',
]

/**
 * The directories the three-layer pipeline occupies.
 *
 * Scoped rather than the whole of `components/chat/`, because the rule below is TRUE
 * here and not everywhere: `controls/` holds modules named for the control they serve
 * (`ExitPlanModeControl.tsx` exports `ExitPlanModeContent`), and that convention is its
 * own. Widening this guard would either fail on eleven files or need an allow-list
 * long enough to stop meaning anything.
 */
const LAYER_DIRS = ['providers', 'model', 'results', 'widgets']

function layerModules(extension: '.ts' | '.tsx'): string[] {
  return LAYER_DIRS.flatMap(dir => collectFiles(join(CHAT_DIR, dir), {
    matches: name => name.endsWith(extension)
      && !name.endsWith('.test.ts')
      && !name.endsWith('.test.tsx')
      && !name.endsWith('.css.ts')
      && !name.endsWith('.fixtures.ts'),
  }))
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

/** Whether the module exports a `const` or `function` with exactly this name. */
function exportsSymbol(source: string, name: string): boolean {
  return new RegExp(`^export (?:const|function) ${name}\\b`, 'm').test(source)
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

/** Each forbidden assertion in one TypeScript source, with its line. */
function assertionsTo(source: string, types: string[]): Array<{ type: string, line: number }> {
  const file = ts.createSourceFile('assertionProbe.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const found: Array<{ type: string, line: number }> = []
  const forbiddenTypes = new Set(types)

  function rightmostTypeName(name: ts.EntityName): string {
    return ts.isIdentifier(name) ? name.text : rightmostTypeName(name.right)
  }

  function containsForbiddenType(type: ts.TypeNode): boolean {
    let forbidden = false
    function inspect(node: ts.Node): void {
      if (ts.isTypeReferenceNode(node) && forbiddenTypes.has(rightmostTypeName(node.typeName)))
        forbidden = true
      if (ts.isImportTypeNode(node) && node.qualifier !== undefined && forbiddenTypes.has(rightmostTypeName(node.qualifier)))
        forbidden = true
      if (!forbidden)
        ts.forEachChild(node, inspect)
    }
    inspect(type)
    return forbidden
  }

  function visit(node: ts.Node): void {
    if (ts.isAsExpression(node) || ts.isTypeAssertionExpression(node)) {
      const assertedType = node.type.getText(file)
      if (containsForbiddenType(node.type)) {
        found.push({
          type: assertedType,
          line: file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1,
        })
      }
    }
    ts.forEachChild(node, visit)
  }

  ts.forEachChild(file, visit)
  return found
}

describe('the chat render pipeline holds the structure its guards assume', () => {
  it('finds the modules it guards', () => {
    expect(layerModules('.tsx').length).toBeGreaterThan(40)
    expect(providerModules().length).toBeGreaterThan(30)
  })

  it('keeps JSX out of the local chat model', () => {
    expect(collectFiles(MODEL_DIR, { matches: name => name.endsWith('.tsx') })).toEqual([])
  })

  // One file per kind: the pair a kind declares has exactly one home, and a kind
  // added to TOOL_KINDS without its file is a compile error the table below turns
  // into a named failure.
  it('holds exactly one kind file per tool kind', () => {
    const SHARED_SHAPE_FILES = new Set(['index.ts', 'generic.ts', 'fileChange.ts'])
    const kindFiles = collectFiles(join(MODEL_DIR, 'tools'), { matches: name => name.endsWith('.ts') })
      .map(file => basename(file))
      .filter(name => !SHARED_SHAPE_FILES.has(name) && !name.endsWith('.test.ts') && !name.endsWith('.typecheck.ts'))
    const fileForKind = (kind: string): string => {
      if (kind === 'unspecified')
        return 'unspecified.ts'
      if (kind === 'switch_mode')
        return 'switchMode.ts'
      if (kind === 'web_search')
        return 'webSearch.ts'
      return `${kind}.ts`
    }
    const expected = TOOL_KINDS.map(fileForKind)
    expect(kindFiles.sort(), 'Every model/tools/<file>.ts is one TOOL_KINDS member\'s home.').toEqual([...expected].sort())
  })

  /**
   * No provider keeps a `renderers/` directory.
   *
   * Layer 1 returns model and never draws -- the ESLint JSX ban enforces that on
   * the markup itself. Four providers once carried a `renderers/` directory from
   * before the pipeline closed, holding modules that answer `TurnEnd` and
   * `NotificationEntry`. Those read the provider's own bytes, so they are
   * extraction, and the name sent every reader to the wrong layer.
   */
  it('keeps no renderers directory under a provider', () => {
    const offences = readdirSync(PROVIDERS_DIR, { withFileTypes: true })
      .filter(entry => entry.isDirectory() && existsSync(join(PROVIDERS_DIR, entry.name, 'renderers')))
      .map(entry => `providers/${entry.name}/renderers`)
    expect(
      offences,
      'A module that reads the provider\'s bytes into model belongs in `extractors/`. '
      + 'Nothing in the provider layer draws.',
    ).toEqual([])
  })

  /**
   * A PascalCase `.tsx` module is ONE component, and carries that component's name.
   *
   * The inverse is what the layers use for everything else: a camelCase module is named
   * after the model shape it reads or draws, so `model/searchResult.ts` and
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
      + 'matching the model shape it draws.',
    ).toEqual([])
  })

  /**
   * The matcher samples below pin the classify walk on sources of their own, because a
   * matcher that reads no hook reports no offence and passes.
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

  /**
   * Every exception the ESLint config carves out stays pinned to a path that exists.
   *
   * Left behind after a module moves, an exception would silently forgive whatever
   * new file takes that path -- the linter rule would read as green while guarding
   * nothing.
   */
  it('keeps every drawing exception pinned to a file that exists', () => {
    const present = new Set(providerModules().map(file => posixRelative(PROVIDERS_DIR, file)))
    const stale = DRAWING_ALLOWED.filter(entry => !present.has(entry))
    expect(stale, 'Delete the entry in `eslint.config.ts`, or repoint it at the module that replaced it.').toEqual([])
  })

  it('keeps the provider-icon exception pinned to a file that exists', () => {
    const srcRoot = join(frontendRoot, 'src')
    const present = new Set(collectFiles(srcRoot, {
      matches: name => (name.endsWith('.ts') || name.endsWith('.tsx')) && !name.endsWith('.d.ts'),
    }).map(file => posixRelative(srcRoot, file)))
    const stale = PROVIDER_COMPARISON_ALLOWED.filter(entry => !present.has(entry))
    expect(stale, 'Delete the entry in `eslint.config.ts`, or repoint it at the module that replaced it.').toEqual([])
  })

  it('keeps the model builder to the single assertion its check earns', () => {
    const builder = join(CHAT_DIR, MODEL_BUILDER)
    expect(existsSync(builder), `\`${MODEL_BUILDER}\` is the one file the assertion ban exempts; the ESLint config entry is stale without it.`).toBe(true)
    const found = assertionsTo(readFileSync(builder, 'utf8'), FORBIDDEN_ASSERTION_TYPES)
    expect(found.map(entry => `${MODEL_BUILDER}:${entry.line} asserts to ${entry.type}`)).toHaveLength(1)
  })

  it('keeps the provider registry to one resolved-content assertion', () => {
    const builder = join(CHAT_DIR, RESOLVED_CONTENT_BUILDER)
    expect(existsSync(builder), `\`${RESOLVED_CONTENT_BUILDER}\` is the one file the resolved-content assertion ban exempts.`).toBe(true)
    const found = assertionsTo(readFileSync(builder, 'utf8'), ['ResolvedMessageContent'])
    expect(found.map(entry => `${RESOLVED_CONTENT_BUILDER}:${entry.line} asserts to ${entry.type}`)).toHaveLength(1)
  })

  it('finds direct and nested TypeScript assertions in a builder exception', () => {
    const found = assertionsTo([
      'const byAs = value as ToolCall',
      'const byAngle = <ToolCall<\'read\'>>value',
      'const qualified = value as ChatIR.ToolCall',
      'const wrapped = value as Readonly<ToolCall>',
      'const imported = value as import(\'./createToolCall\').ToolCall',
    ].join('\n'), FORBIDDEN_ASSERTION_TYPES)
    expect(found.map(entry => entry.line)).toEqual([1, 2, 3, 4, 5])
  })
})
