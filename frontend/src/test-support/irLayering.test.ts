import { existsSync, readFileSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { TOOL_KINDS } from '~/components/chat/ir/toolKind'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'
import { moduleImportEdges } from '~/test-support/typescriptImports'

// The layering guard for `src/components/chat/ir/` -- layer 2 of the chat render
// pipeline. A provider plugin (layer 1) produces the IR, and the shared renderers
// (layer 3) draw it, so the IR itself must depend on NEITHER.
//
// The rule is about the RUNTIME edge. A type import is erased by the compiler, so it
// closes no cycle and pulls no module into the bundle; a value import does both. This
// guard therefore reads the two differently, which is what lets `ReadReminder` keep
// its `AlertVariant` from a component module while nothing in `ir/` can ever CALL one.
//
// It exists because the failure is silent and remote: an `ir/` module that imports a
// component closes an import cycle, and the bundler then reports an unrelated module
// failing to load. `noImportCycles.test.ts` catches the closed cycle; this catches the
// import that is about to close one.

const IR_DIR = join(frontendRoot, 'src/components/chat/ir')

/** Every `import`/`export ... from` in one module, with its specifier and its kind. */
interface ModuleImport {
  specifier: string
  typeOnly: boolean
  line: number
}

/**
 * Every `import` / `export ... from` in one module, with its specifier and its kind.
 *
 * Read from the compiler's syntax tree (`typescriptImports.ts`): every form the
 * grammar holds -- static, re-export, side-effect, `import()`, `require()`, either
 * quote style -- states its edge and its opening line, and no comment is ever read.
 * The line a wrapped statement begins on is the tree's own answer, which the
 * look-back heuristic this replaces could only approximate.
 */
function moduleImports(source: string, fileName: string): ModuleImport[] {
  return moduleImportEdges(source, fileName).map(edge => ({ specifier: edge.specifier, typeOnly: edge.typeOnly, line: edge.line }))
}

/** The `ir/` modules a guard reads: the implementation, never its own tests. */
function irModules(): string[] {
  return collectFiles(IR_DIR, {
    matches: name => (name.endsWith('.ts') || name.endsWith('.tsx')) && !name.endsWith('.test.ts') && !name.endsWith('.test.tsx'),
  })
}

/** Where a specifier resolves for the purposes of this guard. */
function isProviderModule(specifier: string): boolean {
  return specifier.includes('providers/') || specifier.startsWith('../providers')
}

function isComponentModule(specifier: string): boolean {
  return specifier.endsWith('.tsx')
}

function isStylesheet(specifier: string): boolean {
  return specifier.endsWith('.css') || specifier.endsWith('.css.ts')
}

/**
 * Whether one resolved path is a module `ir/` holds.
 *
 * The path must EXIST, because a path alone proves nothing: `../providers/claude`
 * from `ir/tools/` resolves to `ir/providers/claude`, which is inside the
 * directory and is no module of it.
 */
function irModuleExists(resolved: string): boolean {
  if (!resolved.startsWith(`${IR_DIR}/`))
    return false
  return [`${resolved}.ts`, `${resolved}.tsx`, join(resolved, 'index.ts')].some(candidate => existsSync(candidate))
}

/**
 * A module the IR must not reach in ANY form, type import included.
 *
 * A component, a store and an icon library are all the RENDER layer's or the
 * STATE layer's, and a type from one of them puts a decision that belongs there
 * into the description of a row. The IR states what a row means; a `LucideIcon`
 * field states which glyph draws it, and an `AlertVariant` field states which
 * palette colours it. Both were here, and both became IR-owned vocabularies that
 * `results/` maps onto components (`ToolIconHint`, `NotificationIconHint`,
 * `ReminderSeverity`).
 *
 * A type import costs nothing at run time, so this is not the cycle rule below.
 * It is the ownership rule: with it, no renaming of a component prop and no swap
 * of the icon set can reach a module that reads a provider's bytes.
 */
function isPresentationOrStateModule(specifier: string): boolean {
  return specifier.startsWith('~/components/')
    || specifier.startsWith('~/stores/')
    || specifier === 'lucide-solid'
    || specifier.startsWith('lucide-solid/')
    || isComponentModule(specifier)
    || isStylesheet(specifier)
}

/** A VALUE import is allowed from these roots alone, plus a module inside `ir/`. */
function valueImportAllowed(specifier: string, file: string): boolean {
  if (specifier.startsWith('~/lib/') || specifier.startsWith('~/generated/') || specifier.startsWith('~/models/'))
    return true
  // The pure diff modules under `chat/diff/`, which hold no component. Listed one by
  // one rather than by prefix: `../diff/DiffViewer` is a component and stays out.
  // This branch precedes the one below, because each one resolves OUTSIDE `ir/`.
  if (specifier === '../diff/diffBuilder' || specifier === '../diff/diffTypes' || specifier === '../diff/unifiedDiffParser')
    return true
  // Any module `ir/` itself owns, at any depth: `./toolCall` from `ir/`, and
  // `../collapse` from `ir/tools/`. The test is the module the specifier LANDS on
  // rather than how it is spelled, so `../rendererUtils` from `ir/derivations.ts`
  // -- one name and no separator, the same shape as `../collapse` -- still fails.
  if (specifier.startsWith('./') || specifier.startsWith('../'))
    return irModuleExists(resolve(dirname(file), specifier))
  return false
}

describe('the chat row IR layer', () => {
  it('finds the modules it guards', () => {
    expect(irModules().length).toBeGreaterThan(10)
  })

  it('imports nothing from a provider, in any form', () => {
    const offences: string[] = []
    for (const file of irModules()) {
      const source = readFileSync(file, 'utf8')
      for (const entry of moduleImports(source, basename(file))) {
        if (isProviderModule(entry.specifier))
          offences.push(`${posixRelative(frontendRoot, file)}:${entry.line} imports ${entry.specifier}`)
      }
    }
    expect(offences, 'A provider import inverts the layering: the IR is what every provider produces, so it cannot read one.').toEqual([])
  })

  it('imports nothing from a component, a store or an icon library, in any form', () => {
    const offences: string[] = []
    for (const file of irModules()) {
      const source = readFileSync(file, 'utf8')
      for (const entry of moduleImports(source, basename(file))) {
        if (isPresentationOrStateModule(entry.specifier))
          offences.push(`${posixRelative(frontendRoot, file)}:${entry.line} imports ${entry.specifier}`)
      }
    }
    expect(
      offences,
      'A presentation or store type in the IR makes a render-layer decision part of what a row MEANS. '
      + 'Declare a closed hint union here and map it onto the component in `results/`, the way `ToolIconHint` does. '
      + 'A neutral model both layers share belongs in `~/models/`.',
    ).toEqual([])
  })

  it('takes no VALUE import from a component, a stylesheet or a store', () => {
    const offences: string[] = []
    for (const file of irModules()) {
      const source = readFileSync(file, 'utf8')
      for (const entry of moduleImports(source, basename(file))) {
        if (entry.typeOnly)
          continue
        const { specifier } = entry
        if (isComponentModule(specifier) || isStylesheet(specifier) || specifier.startsWith('~/stores/') || !valueImportAllowed(specifier, file))
          offences.push(`${posixRelative(frontendRoot, file)}:${entry.line} imports ${specifier}`)
      }
    }
    expect(
      offences,
      'A value import from the render layer closes an import cycle, which the bundler reports as an unrelated module failing to load. '
      + 'Move the pure helper into `ir/` beside the type it serves, or make the import type-only.',
    ).toEqual([])
  })

  // Every guarded module is a plain `.ts`. A `.tsx` in `ir/` would compile JSX, which
  // is the render layer by definition.
  it('holds no component module of its own', () => {
    const jsx = irModules().filter(file => file.endsWith('.tsx')).map(file => basename(file))
    expect(jsx, 'The IR describes a row; it never draws one.').toEqual([])
  })

  it('lets a kind payload module read a sibling above it, and nothing else above', () => {
    // The `../` extension exists for one shape: `ir/tools/<kind>.ts` reading a sibling
    // of its parent directory. A specifier that resolves outside `ir/` still fails.
    expect(valueImportAllowed('../collapse', join(IR_DIR, 'tools', 'generic.ts'))).toBe(true)
    expect(valueImportAllowed('../providers/claude/extractors/toolCall', join(IR_DIR, 'tools', 'agent.ts'))).toBe(false)
    expect(valueImportAllowed('../../results/ToolMessage', join(IR_DIR, 'tools', 'read.ts'))).toBe(false)
    // The same spelling from one directory higher lands OUTSIDE `ir/`, and the
    // render layer is exactly what sits there.
    expect(valueImportAllowed('../rendererUtils', join(IR_DIR, 'derivations.ts'))).toBe(false)
    expect(valueImportAllowed('./toolCall', join(IR_DIR, 'derivations.ts'))).toBe(true)
  })

  // One file per kind: the pair a kind declares has exactly one home, and a kind
  // added to TOOL_KINDS without its file is a compile error the table below turns
  // into a named failure.
  it('holds exactly one kind file per tool kind', () => {
    const SHARED_SHAPE_FILES = new Set(['index.ts', 'generic.ts', 'fileChange.ts'])
    const kindFiles = collectFiles(join(IR_DIR, 'tools'), { matches: name => name.endsWith('.ts') })
      .map(file => basename(file))
      .filter(name => !SHARED_SHAPE_FILES.has(name) && !name.endsWith('.test.ts'))
    const fileForKind = (kind: string): string => {
      if (kind === '')
        return 'none.ts'
      if (kind === 'switch_mode')
        return 'switchMode.ts'
      if (kind === 'web_search')
        return 'webSearch.ts'
      return `${kind}.ts`
    }
    const expected = TOOL_KINDS.map(fileForKind)
    expect(kindFiles.sort(), 'Every ir/tools/<file>.ts is one TOOL_KINDS member\'s home.').toEqual([...expected].sort())
  })
})
