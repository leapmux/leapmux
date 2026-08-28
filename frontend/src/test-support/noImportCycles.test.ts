import { existsSync, readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

// Module-graph guard: no import cycle in `src/`.
//
// A cycle is not a style problem here. It decides WHEN an imported binding
// holds its value. `~/api/clients` builds every service client at module
// evaluation time -- `createClient(AuthService, transport)` runs in the module
// body, not in a function -- so it reads `transport` the instant the bundler
// evaluates it. Put that module in a cycle and the bundler must evaluate one
// of the two first; the second one reads the other's binding before it is
// assigned and captures `undefined`.
//
// That already happened. `~/api/transport` imported the step-up marker from
// `~/lib/elevation`, which imports `~/api/clients` for the ceremony RPCs,
// which imports `~/api/transport`. Every client in the app was then built on
// an undefined transport, and the app failed at the FIRST RPC with
// "Cannot read properties of undefined (reading 'unary')" -- the system-info
// bootstrap. The login form stayed disabled with no captcha widget and no
// error, because a form that never learns the captcha policy fails closed.
//
// Nothing caught it before the browser. The unit tests mock `~/api/clients`,
// so the cycle never formed under vitest; only the built bundle showed it, in
// a 12-minute E2E run, as a broken login page. This guard is that failure in
// milliseconds, and it covers the whole class rather than the one edge:
// putting the marker in a leaf module fixed this instance, and any future
// import into `~/api` from a module that `~/api` reaches would recreate it.
//
// Only STATIC imports count. A dynamic `import()` resolves after evaluation,
// so it cannot capture an unassigned binding, and counting it would flag every
// lazily loaded route.

const srcRoot = join(frontendRoot, 'src')

/**
 * A static `import ... from '<spec>'` or `export ... from '<spec>'`, with the
 * statement head captured so the caller can drop a type-only form.
 *
 * TypeScript erases `import type { X } from './x'` completely, so it creates
 * no runtime edge and cannot cause a cycle. Counting it would report cycles
 * that do not exist -- a pair of modules that refers to each other's types is
 * both common and harmless.
 */
const FROM_IMPORT = /(?:^|\n)[ \t]*((?:import|export)\b[^;\n]+?)\bfrom\s+['"]([^'"]+)['"]/g

/** A side-effect import: `import './x'`. It evaluates the module, so it counts. */
const BARE_IMPORT = /(?:^|\n)[ \t]*import\s+['"]([^'"]+)['"]/g

const TYPE_ONLY = /\b(?:import|export)\s+type\b/

const SOURCE_EXTENSIONS = ['.ts', '.tsx', '.js', '.jsx']

/**
 * The directories whose contents are not hand-written source: generated proto
 * stubs and the vendored spinner data. A cycle there is not a defect anyone
 * can fix in this repo.
 */
const NOT_HAND_WRITTEN: ReadonlySet<string> = new Set(['generated', 'spinners'])

/**
 * The ONE cycle this guard tolerates, as the single edge that closes it, in
 * repo-relative paths: `from` imports `to`, and `to` already reaches `from`.
 *
 * `CollapsibleContent` takes `JsonHighlightHtml` from `toolRenderers`, and
 * `toolRenderers` renders `CollapsibleContent`. Both bindings are Solid
 * components, read when a component renders and never during module
 * evaluation, so neither can capture the other unassigned. Exempting the edge
 * rather than the two cycle paths keeps the exemption at one line: a third
 * path through the same pair would otherwise need its own entry.
 *
 * It is accidental, not structural. `JsonHighlightHtml` is a 12-line wrapper
 * that sets `lang="json"` on the private `AsyncHighlightedCode`; moving that
 * pair into their own module deletes this entry. That move carries the token
 * gate and the render context with it, which is a chat-renderer change and
 * belongs in its own review.
 */
const ALLOWED_EDGES: ReadonlySet<string> = new Set([
  'src/components/chat/results/CollapsibleContent.tsx -> src/components/chat/toolRenderers.tsx',
])

function edgeKey(from: string, to: string): string {
  return `${from} -> ${to}`
}

/**
 * The file a specifier points at, as an absolute path, or null when it
 * resolves outside `src/` (a package) or matches nothing.
 *
 * Mirrors the resolution the bundler applies to the two forms this repo
 * writes: the `~/` alias for an absolute path under `src/`, and a relative
 * path. Both may omit the extension, and both may name a directory that
 * carries an `index` file.
 */
function resolveSpecifier(specifier: string, fromFile: string): string | null {
  let base: string
  if (specifier.startsWith('~/'))
    base = join(srcRoot, specifier.slice(2))
  else if (specifier.startsWith('.'))
    base = resolve(dirname(fromFile), specifier)
  else
    return null // A package. It cannot close a cycle back into `src/`.

  for (const extension of SOURCE_EXTENSIONS) {
    if (existsSync(base + extension))
      return base + extension
  }
  // An explicit extension in the specifier, or a directory with an index.
  if (existsSync(base) && SOURCE_EXTENSIONS.some(e => base.endsWith(e)))
    return base
  for (const extension of SOURCE_EXTENSIONS) {
    const indexFile = join(base, `index${extension}`)
    if (existsSync(indexFile))
      return indexFile
  }
  return null
}

/** Every runtime import edge out of one module, as repo-relative paths. */
function importsOf(file: string): string[] {
  const text = readFileSync(file, 'utf-8')
  const targets = new Set<string>()

  for (const match of text.matchAll(FROM_IMPORT)) {
    if (TYPE_ONLY.test(match[1]))
      continue
    const resolved = resolveSpecifier(match[2], file)
    if (resolved)
      targets.add(posixRelative(frontendRoot, resolved))
  }
  for (const match of text.matchAll(BARE_IMPORT)) {
    const resolved = resolveSpecifier(match[1], file)
    if (resolved)
      targets.add(posixRelative(frontendRoot, resolved))
  }
  return [...targets].sort()
}

let graphCache: Map<string, string[]> | undefined

/**
 * The import graph over the hand-written modules of `src/`, keyed by
 * repo-relative path.
 *
 * This function excludes a test file on both sides. Nothing imports one, so it
 * can never sit on a cycle that the bundler evaluates.
 */
function buildGraph(): Map<string, string[]> {
  if (graphCache !== undefined)
    return graphCache
  const files = collectFiles(srcRoot, {
    matches: name =>
      SOURCE_EXTENSIONS.some(extension => name.endsWith(extension))
      && !name.includes('.test.')
      && !name.includes('.spec.'),
    alsoSkip: NOT_HAND_WRITTEN,
  })
  const graph = new Map<string, string[]>()
  for (const file of files)
    graph.set(posixRelative(frontendRoot, file), importsOf(file))
  graphCache = graph
  return graph
}

/**
 * The cycles in the graph, each as the list of modules it visits with the
 * entry module repeated at the end.
 *
 * A plain colored depth-first search: an edge back to a module still on the
 * stack closes a cycle. Two paths through the same set of modules are one
 * finding, so the set keys the result.
 *
 * A depth-first search always finds a back edge when the graph has a cycle,
 * so this NEVER misses one. It does not enumerate every path through a
 * tangled component: once a module is finished, the search does not explore a
 * later route into it. The report then lists fewer paths than exist, and the
 * guard still fails.
 */
function findCycles(graph: Map<string, string[]>): string[][] {
  const finished = new Set<string>()
  const onStack = new Set<string>()
  const stack: string[] = []
  const cycles = new Map<string, string[]>()

  const visit = (node: string): void => {
    onStack.add(node)
    stack.push(node)
    for (const next of graph.get(node) ?? []) {
      if (!graph.has(next))
        continue
      if (ALLOWED_EDGES.has(edgeKey(node, next)))
        continue
      if (onStack.has(next)) {
        const path = stack.slice(stack.indexOf(next))
        const key = [...path].sort().join('|')
        if (!cycles.has(key))
          cycles.set(key, [...path, next])
      }
      else if (!finished.has(next)) {
        visit(next)
      }
    }
    stack.pop()
    onStack.delete(node)
    finished.add(node)
  }

  for (const node of [...graph.keys()].sort()) {
    if (!finished.has(node))
      visit(node)
  }
  return [...cycles.values()]
}

describe('module graph', () => {
  it('reuses one graph across cases', () => {
    expect(buildGraph()).toBe(buildGraph())
  })

  it('has no import cycle in src/', () => {
    const cycles = findCycles(buildGraph())
    expect(
      cycles.map(cycle => cycle.join('\n     -> ')),
      'An import cycle decides when a binding holds its value: a module that '
      + 'builds something in its body (like ~/api/clients) can read a partner '
      + 'the bundler did not assign yet and capture undefined. Break the cycle '
      + 'by moving the shared symbol into a module that imports neither side.\n'
      + 'Cycles found:',
    ).toEqual([])
  })

  // A resolver that matched nothing would make the cycle test above pass on an
  // EMPTY graph, and pass forever. That is the failure a guard cannot report on
  // itself, so this test asserts each specifier form this repo writes DIRECTLY.
  //
  // Not a floor on the edge count: the two forms are both in heavy use, so
  // losing either one still leaves well over a thousand edges, and a floor low
  // enough to be stable is too low to notice. Losing the `~/` form is the
  // realistic break -- it is the one with a rule behind it -- and it would
  // silently hide most of the module graph from this guard.
  it('resolves every specifier form the repo writes', () => {
    const anyFile = join(srcRoot, 'app.tsx')

    expect(
      resolveSpecifier('~/api/transport', anyFile),
      'the `~/` alias no longer resolves, so most of the module graph is invisible to this guard',
    ).toBe(join(srcRoot, 'api', 'transport.ts'))

    expect(
      resolveSpecifier('./elevationPrompt', join(srcRoot, 'lib', 'elevation.ts')),
      'a relative specifier no longer resolves',
    ).toBe(join(srcRoot, 'lib', 'elevationPrompt.ts'))

    // A directory that carries an index file. `~/lib/crdt` is imported this
    // way, and its barrel was on a cycle until this review.
    expect(resolveSpecifier('~/lib/crdt', anyFile)).toBe(join(srcRoot, 'lib', 'crdt', 'index.ts'))

    // A package cannot close a cycle back into `src/`, so it stays out.
    expect(resolveSpecifier('solid-js', anyFile)).toBeNull()

    // A vanilla-extract stylesheet stays IN. The specifier omits the `.ts`,
    // exactly as the bundler resolves it, but the file behind it is an ordinary
    // TS module with a body that runs -- so it can sit on a cycle like any
    // other, and dropping it would leave a real hazard unscanned.
    expect(resolveSpecifier('./styles/global.css', anyFile))
      .toBe(join(srcRoot, 'styles', 'global.css.ts'))
  })

  // The walk itself, for the same reason: an empty scan passes the cycle check.
  it('scans the source tree', () => {
    expect(
      buildGraph().size,
      'this test scanned no modules; check the walk root and the skip list',
    ).toBeGreaterThan(500)
  })

  // The exemption must stay pinned to a real edge. Left behind after the
  // modules move or the import goes, it would silently forgive whatever new
  // cycle happens to reuse those two paths.
  it('keeps every allowed edge pinned to an import that still exists', () => {
    const graph = buildGraph()
    for (const edge of ALLOWED_EDGES) {
      const [from, to] = edge.split(' -> ')
      expect(graph.get(from), `${from} is listed in ALLOWED_EDGES but no longer exists`).toBeDefined()
      expect(
        graph.get(from),
        `ALLOWED_EDGES exempts "${edge}", but ${from} no longer imports ${to}. Delete the entry.`,
      ).toContain(to)
    }
  })
})
