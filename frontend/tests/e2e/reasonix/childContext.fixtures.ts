/** Build the native explorer context captured from Reasonix v2.59.0. */
export function reasonixChildContextPack(prompt: string): string {
  return `<subagent-context event="SubagentStart">\nBefore acting, check the available skills and tools. If a relevant skill is available, invoke it before continuing. Delegate to another sub-agent only when the task genuinely benefits from isolated context and the delegation tool is available.\n</subagent-context>\n\n${prompt}\n\n<execution-policy preset="balanced" version="3">\nverify=targeted\n</execution-policy>`
}
