import { moduleImportEdges } from './typescriptImports'

// These path predicates select the test runner and validate the E2E layout.
// Their co-located tests check individual paths and the actual source tree.
// Individual cases prevent an empty filesystem result from hiding an incorrect predicate.

/** Matches a vitest unit test: `foo.test.ts`, `foo.test.tsx`. */
const UNIT_TEST = /\.test\.tsx?$/

/** Matches a Playwright spec: `foo.spec.ts`, `foo.spec.tsx`. */
const PLAYWRIGHT_SPEC = /\.spec\.tsx?$/

/** Reports whether `path` is a vitest unit test. */
export function isUnitTest(path: string): boolean {
  return UNIT_TEST.test(path)
}

/** Reports whether `path` is a Playwright spec. */
export function isPlaywrightSpec(path: string): boolean {
  return PLAYWRIGHT_SPEC.test(path)
}

/** Accept a relative browser-spec path at the root or directly inside a known provider directory. */
export function isAllowedE2ESpecPath(path: string, providers: ReadonlySet<string>): boolean {
  const parts = path.replaceAll('\\', '/').split('/')
  if (parts.some(part => part === '' || part === '.' || part === '..') || !path.endsWith('.spec.ts'))
    return false
  return parts.length === 1 || (parts.length === 2 && providers.has(parts[0]!))
}

/** Find direct provider dependencies that make a root browser spec provider-specific. */
export function providerSpecDependencies(source: string, providers: ReadonlySet<string>, fixtures: ReadonlySet<string>): string[] {
  return moduleImportEdges(source).flatMap(({ specifier }) => {
    const module = specifier.replace(/\.(?:ts|tsx)$/, '')
    if (fixtures.has(module))
      return [specifier]
    if (module.startsWith('./') && providers.has(module.slice(2).split('/')[0]!))
      return [specifier]
    return []
  })
}

/**
 * Return the sibling module of a co-located unit test: `mail.test.ts` -> `mail.ts`.
 * The pattern matches the path suffix, so a directory that contains `.test.` stays unchanged.
 * Other paths stay unchanged. Callers filter with `isUnitTest` before checking whether the sibling exists.
 */
export function siblingModulePath(testPath: string): string {
  return testPath.replace(/\.test(\.tsx?)$/, '$1')
}
