const modules = import.meta.glob('/src/spinners/*.json', { eager: true })

const allVerbs: string[] = []
for (const mod of Object.values(modules)) {
  const file = (mod as any).default ?? mod
  if (file.spinnerVerbs?.verbs)
    allVerbs.push(...file.spinnerVerbs.verbs)
}

let lastVerb: string | undefined

export function getRandomVerb(): string {
  if (allVerbs.length === 0)
    return 'Thinking'
  if (allVerbs.length === 1)
    // The length check keeps the index in range; `?? 'Thinking'` is the type-level guard alone.
    return allVerbs[0] ?? 'Thinking'
  let verb: string
  do {
    // The index is modulo a non-empty list; `?? 'Thinking'` is the type-level guard alone.
    verb = allVerbs[Math.floor(Math.random() * allVerbs.length)] ?? 'Thinking'
  } while (verb === lastVerb)
  lastVerb = verb
  return verb
}
