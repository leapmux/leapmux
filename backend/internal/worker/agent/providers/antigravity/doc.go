// Package antigravity runs Antigravity CLI through `agy acp`. ACP stdio
// authenticates and starts the backend. The language server owns cascade
// lifecycle, prompts, model configuration, approvals, and live streams.
// ACP alone supplies no wake-turn or subagent approvals between prompts, and
// `agy --hub` does not enforce the strict tool configuration this provider sends.
package antigravity
