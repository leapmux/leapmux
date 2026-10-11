import type { Locator, Page } from '@playwright/test'

// A locator that Playwright's own `expect` reads, for the unit test of an E2E helper that asserts on the page.
//
// Playwright takes an object as a locator when its `_apiName` is `Locator`, and it reads the state of the locator
// through `_expect(expression, options)`. In a real run the browser waits there until the state matches or the
// assertion times out. A fake answers at once, so an assertion on it passes or fails at once.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its own. Its cases live in
// `fakeLocator.test.ts` beside it.

/** One state check that Playwright's `expect` sends to a locator, as the `_expect` method of the locator receives it. */
export interface LocatorCheck {
  /** The check, such as `to.be.visible`, `to.have.count`, or `to.have.attribute.value`. */
  expression: string
  /** Whether the assertion is negated. The answer states the state, not the negation. */
  isNot: boolean
  timeout?: number
  /** The argument of the check, such as the attribute name of `to.have.attribute.value`. */
  expressionArg?: unknown
  /** The count of `toHaveCount`. */
  expectedNumber?: number
  expectedValue?: unknown
  /** The text of a text or attribute check. */
  expectedText?: ReadonlyArray<{ string?: string }>
}

/**
 * The answer to one check: whether the state matches the check, and the value that a failure message prints as the
 * received value. A boolean answer prints itself.
 */
export type LocatorCheckAnswer = boolean | { matches: boolean, received?: unknown }

/** Decide each check of a fake locator. */
export type LocatorCheckAnswerer = (check: LocatorCheck) => LocatorCheckAnswer | Promise<LocatorCheckAnswer>

/**
 * Return a locator whose checks `answer` decides, with the other `members` that the helper under test calls, such as
 * `click` or `first`. `answer` states whether the state matches, whatever `isNot` states, as the browser does: for
 * `not.toBeVisible`, a hidden element answers false. The default answer passes every check that is not negated.
 */
export function fakeLocator<T extends object = object>(answer: LocatorCheckAnswerer = () => true, members?: T): Locator & T {
  const locator = {
    _apiName: 'Locator',
    async _expect(expression: string, options: Omit<LocatorCheck, 'expression'>) {
      const answered = await answer({ ...options, expression })
      const { matches, received } = typeof answered === 'boolean' ? { matches: answered, received: answered } : answered
      // Playwright prints `received.value` in a failure message.
      return { matches, received: { value: received ?? matches }, log: [], timedOut: false }
    },
  }
  return Object.assign(locator, members) as unknown as Locator & T
}

/**
 * Return a fake locator that appends each check to `log` as `<name>:<expression>`, with `=<count>` for a count check,
 * and answers through `answer`.
 */
export function recordingLocator<T extends object = object>(name: string, log: string[], answer: LocatorCheckAnswerer = () => true, members?: T): Locator & T {
  return fakeLocator((check) => {
    log.push(`${name}:${check.expression}${check.expectedNumber === undefined ? '' : `=${check.expectedNumber}`}`)
    return answer(check)
  }, members)
}

/** How {@link fakeLocatorTree} answers and what its root page holds. */
export interface FakeLocatorTreeOptions<P extends object> {
  /** Receives each action and each check, with the path of the node: `click <path>`, `[not ]<expression> <path>`. */
  log: string[]
  /** Decide each check, and each `isVisible` read as the expression `isVisible`, from the path. The default is true. */
  answer?: (expression: string, path: string) => boolean
  /** Return the value of an attribute of a node. The default is null. Each read also enters `log` as `read <name>`. */
  attribute?: (name: string, path: string) => string | null
  /** The members of the root page that a node does not have, such as `goto`, `reload`, `context`, or `keyboard`. */
  page?: P
}

/** A fake locator tree: the root page, and the node of any path, for a check that a helper built a path. */
export interface FakeLocatorTree<P extends object> {
  page: Page & P
  node: (path: string) => Locator
}

/**
 * Return a tree of fake locators. Each node has a path that states how the helper under test built it from the root
 * `page`, such as `page >> testid=chip.first`, and logs each action and check with that path.
 */
export function fakeLocatorTree<P extends object = object>(options: FakeLocatorTreeOptions<P>): FakeLocatorTree<P> {
  const { log } = options
  const answer = options.answer ?? (() => true)
  const node = (path: string): Locator => fakeLocator((check) => {
    log.push(`${check.isNot ? 'not ' : ''}${check.expression} ${path}`)
    return answer(check.expression, path)
  }, {
    path,
    locator: (selector: string) => node(`${path} >> ${selector}`),
    getByTestId: (testId: string) => node(`${path} >> testid=${testId}`),
    getByLabel: (label: string) => node(`${path} >> label=${label}`),
    getByText: (text: string, textOptions?: { exact?: boolean }) => node(`${path} >> text=${text}${textOptions?.exact ? ' exact' : ''}`),
    getByRole: (role: string, roleOptions?: { name?: string, exact?: boolean }) =>
      node(`${path} >> role=${role}${roleOptions?.name === undefined ? '' : `[name=${roleOptions.name}${roleOptions.exact ? ' exact' : ''}]`}`),
    first: () => node(`${path}.first`),
    nth: (index: number) => node(`${path}.nth(${index})`),
    filter: (filter: { hasText?: string | RegExp, has?: { path: string }, visible?: boolean }) => {
      const parts = [
        filter.hasText === undefined ? '' : `hasText=${String(filter.hasText)}`,
        filter.has === undefined ? '' : `has=(${filter.has.path})`,
        filter.visible === undefined ? '' : `visible=${filter.visible}`,
      ].filter(Boolean)
      return node(`${path}[${parts.join(' ')}]`)
    },
    or: (other: { path: string }) => node(`(${path} | ${other.path})`),
    click: async () => {
      log.push(`click ${path}`)
    },
    hover: async () => {
      log.push(`hover ${path}`)
    },
    fill: async (value: string) => {
      log.push(`fill ${path} with ${value}`)
    },
    isVisible: async () => answer('isVisible', path),
    getAttribute: async (name: string) => {
      log.push(`read ${name} ${path}`)
      return options.attribute?.(name, path) ?? null
    },
  })
  const page = Object.assign(node('page'), options.page) as unknown as Page & P
  return { page, node }
}
