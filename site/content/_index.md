---
title: LeapMux
layout: hextra-home
---

<div class="hx:mt-6 hx:mb-6">
{{< hextra/hero-headline >}}
  A workspace for a fleet&nbsp;<br class="hx:sm:block hx:hidden" />of coding agents
{{< /hextra/hero-headline >}}
</div>

<div class="hx:mb-8">
{{< hextra/hero-subtitle >}}
  Run Claude Code, Codex, and more side by side &mdash; each in its own git worktree and branch,&nbsp;<br class="hx:sm:block hx:hidden" />tiled or floating, on a local or remote machine, with traffic to your agents end-to-end encrypted.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-8">
{{< hextra/hero-button text="Read the manual" link="/docs/" >}}
{{< hextra/hero-button text="Quick Start" link="/docs/getting-started/quick-start/" style="background:transparent;color:var(--hx-color-primary-600);border:1px solid var(--hx-color-primary-600);margin-left:0.75rem" >}}
{{< hextra/hero-button text="Downloads" link="https://github.com/leapmux/leapmux/releases" style="background:transparent;color:var(--hx-color-primary-600);border:1px solid var(--hx-color-primary-600);margin-left:0.75rem" >}}
</div>

<div class="hx:mb-10">
{{< screenshot src="hero-workspace" alt="LeapMux workspace running Claude Code, Antigravity, Codex, a live file diff viewer, and an integrated PTY terminal across isolated git worktrees" caption="Claude Code, Antigravity, Codex, a syntax-highlighted diff viewer, and a live PTY terminal running side by side in isolated git worktrees." width="1440" height="900" eager="true" >}}
</div>

<div class="hx:mb-10">
  <h2 class="hx:text-xl hx:font-semibold hx:mb-3">Or let your coding agent introduce you</h2>
  <p class="hx:text-gray-600 hx:dark:text-gray-400 hx:mb-4">Paste this prompt into Claude Code, Codex, Cursor, or any other coding agent to pick a setup mode, install LeapMux, and launch your first parallel session.</p>
{{< copy-prompt label="Ask your coding agent to set up LeapMux" >}}
Read https://leapmux.dev/agent-guide.md and help me choose the right LeapMux setup mode for my machine, install it, and start my first parallel coding-agent session.
{{< /copy-prompt >}}
</div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="Many agents, isolated"
    icon="view-grid"
    subtitle="Run several coding agents at once, each in its own branch and worktree, so their changes do not conflict."
    link="/docs/using/coding-agents/"
  >}}
  {{< hextra/feature-card
    title="Git worktrees & branches"
    icon="code"
    subtitle="Open every agent or terminal in a new or existing worktree, with dirty-worktree protection on close."
    link="/docs/using/worktrees-and-branches/"
  >}}
  {{< hextra/feature-card
    title="Integrated terminals"
    icon="terminal"
    subtitle="Full pseudo-terminal (PTY) shells live alongside your agents in the same layout, and persist across reconnects."
    link="/docs/using/terminals/"
  >}}
  {{< hextra/feature-card
    title="End-to-end encrypted"
    icon="lock-closed"
    subtitle="Traffic between your app and your agents uses a hybrid post-quantum handshake. While the Hub only relays that traffic, it cannot read the content."
    link="/docs/admin/security/"
  >}}
  {{< hextra/feature-card
    title="NAT-friendly remote machines"
    icon="server"
    subtitle="The machines running your agents dial out to connect, so they work behind firewalls and NATs with no inbound ports open."
    link="/docs/admin/managing-workers/"
  >}}
  {{< hextra/feature-card
    title="Tiling & floating layout"
    icon="template"
    subtitle="Tile, split, grid, and float your agents and terminals. Layouts sync live across your devices."
    link="/docs/using/tabs-and-layout/"
  >}}
{{< /hextra/feature-grid >}}
