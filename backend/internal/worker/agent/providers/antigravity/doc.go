// Package antigravity runs Antigravity CLI through `agy acp`. ACP stdio opens,
// resumes, and closes sessions. The language server that `agy` hosts then owns
// prompts, model configuration, approvals, and one live stream per cascade.
// ACP alone supplies no wake-turn or subagent approvals between prompts, and
// `agy --hub` does not enforce the strict tool configuration this provider sends.
package antigravity
