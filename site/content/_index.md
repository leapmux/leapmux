---
title: LeapMux
layout: hextra-home
---

<div class="hx:mt-6 hx:mb-6">
{{< hextra/hero-headline >}}
  A workspace for a fleet&nbsp;<br class="hx:sm:block hx:hidden" />of coding agents
{{< /hextra/hero-headline >}}
</div>

<div class="hx:mb-12">
{{< hextra/hero-subtitle >}}
  Run Claude Code, Codex, and more side by side &mdash; each in its own git worktree&nbsp;<br class="hx:sm:block hx:hidden" />and branch, tiled or floating, on a local or remote machine, with traffic to your agents end-to-end encrypted.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-6">
{{< hextra/hero-button text="Read the manual" link="/docs/" >}}
{{< hextra/hero-button text="Downloads" link="https://github.com/leapmux/leapmux/releases" style="background:transparent;color:var(--hx-color-primary-600);border:1px solid var(--hx-color-primary-600);margin-left:0.75rem" >}}
</div>

<div class="hx:mt-6"></div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="Many agents, isolated"
    icon="view-grid"
    subtitle="Run several coding agents at once, each in its own branch and worktree, so their changes don't clobber each other."
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
    subtitle="Full PTY shells live alongside your agents in the same layout, and persist across reconnects."
    link="/docs/using/terminals/"
  >}}
  {{< hextra/feature-card
    title="End-to-end encrypted"
    icon="lock-closed"
    subtitle="Traffic between your browser and your agents is sealed with a hybrid post-quantum handshake. The relay in between only forwards bytes it cannot read."
    link="/docs/operating/security/"
  >}}
  {{< hextra/feature-card
    title="NAT-friendly remote machines"
    icon="server"
    subtitle="The machines running your agents dial out to connect, so they work behind firewalls and NATs with no inbound ports open."
    link="/docs/operating/managing-workers/"
  >}}
  {{< hextra/feature-card
    title="Tiling & floating layout"
    icon="template"
    subtitle="Tile, split, grid, and float your agents and terminals. Layouts sync live across your devices."
    link="/docs/using/tabs-and-layout/"
  >}}
{{< /hextra/feature-grid >}}
