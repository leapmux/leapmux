import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { collectFiles, frontendRoot, posixRelative, resolveSourceSpecifier } from '~/test-support/sourceTree'
import { moduleImportEdges } from '~/test-support/typescriptImports'

// Module-graph guard: code that only a worker bundle may load stays out of the
// browser main bundle.
//
// Each group below moved off the main thread for a reason that one import
// undoes: the import puts the code back in the main bundle, and the next change
// to it runs there. This walks the import graph of the browser main bundle from
// its entry points and refuses each such edge. A `new Worker(new URL(...))` is
// not an import, so the walk does not follow a worker bundle, as the bundler
// does not.

const srcRoot = join(frontendRoot, 'src')

/** One group of code that only a worker bundle may load, and why. */
interface WorkerOnlyGroup {
  /** What the group is, as the test titles state it. */
  name: string
  /** Repo-relative paths of the modules that only a worker may load. */
  modules: readonly string[]
  /** The packages that only those modules need. A subpath of a package counts as the package. */
  packages: readonly string[]
}

const WORKER_ONLY_GROUPS: readonly WorkerOnlyGroup[] = [
  {
    // The Noise session of every channel runs in the channel session worker
    // (src/lib/channelSessionWorker.ts): the handshake, the cipher states, the
    // encryption, the decryption and the rekey. The main thread keeps the socket
    // and the RPC layer, and must never hold a cipher key.
    name: 'the channel session',
    modules: [
      'src/lib/noise.ts',
      'src/lib/noise-hybrid.ts',
      'src/lib/channelSession.ts',
      'src/lib/channelSessionInbound.ts',
      'src/lib/channelSessionEndpoint.ts',
      'src/lib/channelSessionInProcess.ts',
      'src/lib/slhDsaVerifyHere.ts',
    ],
    packages: ['@noble/ciphers', '@noble/curves', '@noble/post-quantum'],
  },
  {
    // The diff worker runs the Myers search. jsdiff declares no `sideEffects`,
    // so the bundler keeps the whole library in any chunk that imports it.
    name: 'the diff search',
    modules: ['src/components/chat/diff/diffSearch.ts'],
    packages: ['diff'],
  },
  {
    // The Markdown worker parses and highlights every Markdown body. The main
    // thread keeps only the stringifier, which escapes the source fallback
    // (markdownSourceFallback.ts) and parses nothing.
    name: 'the Markdown HTML pipeline',
    modules: ['src/lib/markdownProcessor.ts'],
    packages: ['remark-rehype', '@shikijs/rehype'],
  },
]

/** A package import of one module, as the module spells it. */
interface PackageImport {
  specifier: string
  importer: string
}

/** Every module that the runtime import edges reach from `entries`, and every package that they import. */
interface ModuleClosure {
  /** Repo-relative paths. */
  modules: Set<string>
  packages: PackageImport[]
}

/**
 * The closure of the runtime import edges from `entries`: static imports,
 * re-exports and dynamic imports. A type-only edge pulls nothing into a bundle,
 * so the walk skips it.
 */
function closureOf(entries: readonly string[]): ModuleClosure {
  const modules = new Set<string>()
  const packages: PackageImport[] = []
  const queue = [...entries]
  for (let file = queue.pop(); file !== undefined; file = queue.pop()) {
    const importer = posixRelative(frontendRoot, file)
    if (modules.has(importer))
      continue
    modules.add(importer)
    for (const edge of moduleImportEdges(readFileSync(file, 'utf-8'), file)) {
      if (edge.typeOnly)
        continue
      const resolved = resolveSourceSpecifier(edge.specifier, file)
      if (resolved === null)
        packages.push({ specifier: edge.specifier, importer })
      else
        queue.push(resolved)
    }
  }
  return { modules, packages }
}

/** The entry points of the browser main bundle: the client entry, the app, and every route module. */
function mainThreadEntries(): string[] {
  const routes = collectFiles(join(srcRoot, 'routes'), {
    matches: name => /\.tsx?$/.test(name) && !/\.(?:test|spec)\.tsx?$/.test(name),
  })
  return [
    join(srcRoot, 'entry-client.tsx'),
    join(srcRoot, 'app.tsx'),
    ...routes,
  ]
}

describe('the browser main bundle', () => {
  const main = closureOf(mainThreadEntries())

  // Without this, a walk that reached nothing would pass every check below.
  it('reaches the channel manager, the session client and the diff worker client from the browser entries', () => {
    expect(main.modules.has('src/lib/channel.ts'), 'the walk no longer reaches the channel manager').toBe(true)
    expect(main.modules.has('src/lib/channelSessionClient.ts'), 'the walk no longer reaches the session client').toBe(true)
    expect(main.modules.has('src/components/chat/diff/diffWorkerClient.ts'), 'the walk no longer reaches the diff worker client').toBe(true)
  })

  it.each(WORKER_ONLY_GROUPS.map(group => [group.name, group] as const))('reaches none of the modules of %s', (_name, group) => {
    expect(group.modules.filter(module => main.modules.has(module))).toEqual([])
  })

  it.each(WORKER_ONLY_GROUPS.map(group => [group.name, group] as const))('imports none of the packages of %s', (_name, group) => {
    const offending = main.packages.filter(({ specifier }) =>
      group.packages.some(name => specifier === name || specifier.startsWith(`${name}/`)))
    expect(offending).toEqual([])
  })
})

describe('the worker bundles', () => {
  it('reach the endpoint, and neither the client nor the manager, from the session worker', () => {
    const worker = closureOf([
      join(srcRoot, 'lib', 'channelSessionWorker.ts'),
    ])

    expect(worker.modules.has('src/lib/channelSessionEndpoint.ts')).toBe(true)
    expect(worker.modules.has('src/lib/channelSessionClient.ts')).toBe(false)
    expect(worker.modules.has('src/lib/channel.ts')).toBe(false)
  })

  it('reach the diff search from the diff worker', () => {
    const worker = closureOf([
      join(srcRoot, 'components', 'chat', 'diff', 'diffWorker.ts'),
    ])

    expect(worker.modules.has('src/components/chat/diff/diffSearch.ts')).toBe(true)
  })
})
