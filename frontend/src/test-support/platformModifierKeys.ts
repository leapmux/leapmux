import type * as TypeScript from 'typescript'
import {
  createSourceFile,
  forEachChild,
  isArrayLiteralExpression,
  isCallExpression,
  isIdentifier,
  isNoSubstitutionTemplateLiteral,
  isPropertyAccessExpression,
  isPropertyAssignment,
  isStringLiteral,
  isTemplateExpression,
  ScriptTarget,
} from 'typescript'
import { enclosingFunctionName, lineOf } from '~/test-support/syntaxSite'

// The analysis behind the E2E guard that requires `PLATFORM_MOD` for the platform modifier of a key chord.
//
// The app binds its `$mod` chords through tinykeys, which takes Meta on macOS and Control on every other platform.
// `PLATFORM_MOD` in `tests/e2e/helpers/ui.ts` follows the same rule. A fixed `Meta` is a different chord on Linux and
// Windows, and the composer accepts either modifier for a send, so a spec with a fixed `Meta` can pass on macOS and
// test nothing on another platform. ProseMirror selects all on `Mod-a` only, so a fixed `Meta+a` selects nothing on
// Linux and Windows. `ControlOrMeta` is Playwright's own spelling of the same rule. The guard refuses it too, so the
// tree keeps one spelling of the platform modifier.
//
// The analysis reads the syntax tree. It finds a key name in the literal text of a key argument:
//
// - An argument of a method named `press`, `down`, or `up`: `keyboard.press(key)`, `locator.press(key)`, and
//   `page.press(selector, key)`. A selector holds no `+`-joined key name, so a check of each argument is safe.
// - An element of a `modifiers` array (`locator.click({ modifiers: [...] })`).
//
// A string literal and each static part of a template literal count, so `Meta+${key}` is a finding and
// `${PLATFORM_MOD}+Enter` is not. A key that a variable holds is not a literal, so the analysis cannot see it.
//
// Separate from the guard, as `fixedWaits.ts` is: a guard over the real tree asserts an EMPTY list, and an analysis
// that finds nothing passes it forever. This function takes sources and returns findings, so a case states what it
// must find.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its own. Its cases live in
// `platformModifierKeys.test.ts` beside it.

/** The key names that the guard refuses in place of `PLATFORM_MOD`: each name of the Meta key, and `ControlOrMeta`. */
export const REFUSED_MODIFIER_KEYS: readonly string[] = ['Meta', 'MetaLeft', 'MetaRight', 'ControlOrMeta']

/** The methods whose first argument is a key or a chord. */
const KEY_METHODS: ReadonlySet<string> = new Set(['press', 'down', 'up'])

/** One source file of the analysis. */
export interface ModifierSourceFile {
  /** The absolute path. */
  path: string
  source: string
}

/** One key argument that spells a refused modifier. */
export interface RefusedModifierKey {
  path: string
  /** The 1-based line of the key argument. */
  line: number
  /** The name of the function that holds the key argument, or '' when no named function holds it. */
  enclosingFunction: string
  /** The refused key name. */
  key: string
}

/** Return the refused key name in the literal text of `node`, or undefined when it spells none. */
function refusedKey(node: TypeScript.Node): string | undefined {
  let texts: string[]
  if (isStringLiteral(node) || isNoSubstitutionTemplateLiteral(node))
    texts = [node.text]
  else if (isTemplateExpression(node))
    texts = [node.head.text, ...node.templateSpans.map(span => span.literal.text)]
  else
    return undefined
  for (const text of texts) {
    // A chord joins its keys with `+`, as in `Shift+Meta+ArrowLeft`.
    const key = text.split('+').find(part => REFUSED_MODIFIER_KEYS.includes(part))
    if (key !== undefined)
      return key
  }
  return undefined
}

/** Return the key arguments of `node`: the arguments of a key method, or the elements of a `modifiers` array. */
function keyArguments(node: TypeScript.Node): readonly TypeScript.Node[] {
  if (isCallExpression(node) && isPropertyAccessExpression(node.expression) && KEY_METHODS.has(node.expression.name.text))
    return node.arguments
  if (isPropertyAssignment(node) && isIdentifier(node.name) && node.name.text === 'modifiers' && isArrayLiteralExpression(node.initializer))
    return node.initializer.elements
  return []
}

/** Find each key argument in `inputs` that spells a refused modifier. */
export function refusedModifierKeys(inputs: readonly ModifierSourceFile[]): RefusedModifierKey[] {
  const findings: RefusedModifierKey[] = []
  for (const input of inputs) {
    const file = createSourceFile(input.path, input.source, ScriptTarget.Latest, /* setParentNodes */ true)
    const visit = (node: TypeScript.Node): void => {
      for (const argument of keyArguments(node)) {
        const key = refusedKey(argument)
        if (key !== undefined)
          findings.push({ path: input.path, line: lineOf(file, argument), enclosingFunction: enclosingFunctionName(argument), key })
      }
      forEachChild(node, visit)
    }
    visit(file)
  }
  return findings
}
