const modules = import.meta.glob('/public/spinners/*.json', { eager: true })

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
    return allVerbs[0]
  let verb: string
  do {
    verb = allVerbs[Math.floor(Math.random() * allVerbs.length)]
  } while (verb === lastVerb)
  lastVerb = verb
  return verb
}
