---
title: LeapMux
layout: hextra-home
---

<div class="hx:mt-6 hx:mb-4 hero-headline-with-ticker">
{{< hextra/hero-headline >}}
  <span class="hero-headline-line hero-headline-line--top">A workspace for a fleet</span> <span class="hero-headline-line hero-headline-line--bottom">of coding agents</span>
{{< /hextra/hero-headline >}}
{{< agent-ticker >}}
</div>

<div class="hx:mb-6">
{{< hextra/hero-subtitle >}}
  Run Claude Code, Codex, and {{< agent-provider-count >}} coding agents side by side &mdash; each in its own git worktree and branch,&nbsp;<br class="hx:sm:block hx:hidden" />tiled or floating, on a local or remote machine, with end-to-end encrypted traffic.
{{< /hextra/hero-subtitle >}}
</div>

<div class="lm-hero-actions">
  <a class="lm-btn-primary" id="lm-home-download-cta" href="/download/">Download LeapMux</a>
  <a class="lm-btn-secondary" href="/docs/getting-started/quick-start/">5-Minute Quick Start</a>
  <a class="lm-btn-secondary" href="/docs/">Read the Docs</a>
</div>

<p class="lm-hero-meta">
  macOS, Linux, Windows &amp; browser &middot; Bring your own agent subscriptions &middot; <a href="/docs/reference/supported-agents/">Compare all {{< agent-provider-count >}} supported agents</a>
</p>

<script>
(() => {
  const btn = document.getElementById('lm-home-download-cta');
  if (!btn) return;
  const hint = (((navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || '') + ' ' + (navigator.userAgent || '')).toLowerCase();
  if (hint.includes('mac')) btn.textContent = 'Download for macOS';
  else if (hint.includes('win')) btn.textContent = 'Download for Windows';
  else if (hint.includes('linux')) btn.textContent = 'Download for Linux';
})();
</script>

<div class="hx:mb-8">
{{< hero-showcase >}}
</div>

<div class="lm-proof-strip">
  <div class="lm-proof-item">
    <div class="lm-proof-value">{{< agent-provider-count >}} coding agents</div>
    <div class="lm-proof-label">Unified chat, diffs, queueing, and permissions backed by an E2E-verified capability matrix.</div>
  </div>
  <div class="lm-proof-item">
    <div class="lm-proof-value">Hybrid post-quantum E2EE</div>
    <div class="lm-proof-label">Your Hub relays ciphertext between clients and Workers and cannot read agent chats, terminal output, or files.</div>
  </div>
  <div class="lm-proof-item">
    <div class="lm-proof-value">Zero inbound ports</div>
    <div class="lm-proof-label">Remote Workers on Linux servers or GPU boxes dial out over WebSockets through firewalls and NAT.</div>
  </div>
  <div class="lm-proof-item">
    <div class="lm-proof-value">Cross-platform &amp; self-hosted</div>
    <div class="lm-proof-label">Native desktop app plus browser UI for macOS, Linux, and Windows under FSL-1.1-ALv2 (Apache 2.0 after 2 years).</div>
  </div>
</div>

<div class="hx:mb-10">
  <h2 class="hx:text-xl hx:font-semibold hx:mb-3">Or let your coding agent introduce you</h2>
  <p class="hx:text-gray-600 hx:dark:text-gray-400 hx:mb-4">Paste this prompt into Claude Code, Codex, Cursor, or any other coding agent to pick a deployment mode, install LeapMux, and launch your first parallel session.</p>
{{< copy-prompt label="Ask your coding agent to set up LeapMux" >}}
Read https://leapmux.dev/agent-guide.md and help me choose the right LeapMux setup mode for my machine, install it, and start my first parallel coding-agent session.
{{< /copy-prompt >}}
</div>

<div class="lm-showcase">
  <section class="lm-showcase-row">
    <div class="lm-showcase-copy">
      <div class="lm-showcase-eyebrow">Parallel Worktrees &amp; Layout</div>
      <h2 class="lm-showcase-title">Parallel agents, zero collisions</h2>
      <p class="lm-showcase-text">Open each agent in its own git worktree and branch from one dialog. Tile agents beside a syntax-highlighted diff viewer and live PTY terminals, steer running turns mid-flight, and clean up worktrees safely when you close the last tab.</p>
      <ul class="lm-showcase-list">
        <li>Create isolated git worktrees in one click</li>
        <li>Queue prompts and steer or preempt mid-turn</li>
        <li>Inspect unified and split diffs side by side</li>
        <li>Guard dirty worktrees before closing tabs</li>
      </ul>
      <a class="lm-showcase-link" href="/docs/recipes/parallel-agents/">Read the parallel agents recipe &rarr;</a>
    </div>
    <div class="lm-showcase-media">
      {{< video-clip src="parallel-worktrees" poster="tabs-tiling-layout" alt="Animated walkthrough of switching between parallel coding agents in isolated git worktrees, toggling Split diff view, and opening the Quake terminal" caption="Switch between isolated worktrees, toggle split diffs, and pop down a Quake terminal without losing focus." width="1280" height="800" >}}
    </div>
  </section>

  <section class="lm-showcase-row lm-showcase-row--reverse">
    <div class="lm-showcase-copy">
      <div class="lm-showcase-eyebrow">Remote Workers &amp; E2EE</div>
      <h2 class="lm-showcase-title">Encrypted devboxes behind NAT</h2>
      <p class="lm-showcase-text">Start solo on your laptop with one click, or pair a Hub with remote Workers on cloud VMs, office desktops, or GPU servers. Workers dial out over HTTPS/WebSocket, so you open zero inbound ports on your devboxes.</p>
      <ul class="lm-showcase-list">
        <li>Hybrid post-quantum E2EE to every Worker</li>
        <li>Live layout sync across desktop and browser</li>
        <li>Daemon sessions survive browser closes</li>
        <li>Outbound WebSockets with zero open ports</li>
      </ul>
      <a class="lm-showcase-link" href="/docs/recipes/remote-devbox/">Connect a remote devbox &rarr;</a>
    </div>
    <div class="lm-showcase-media">
      {{< video-clip src="remote-workers-tunnels" poster="register-worker-dialog" alt="Animated walkthrough of inspecting the online gpu-devbox Worker menu and minting POSIX or PowerShell registration commands" caption="Inspect connected remote Workers and register new machines behind NAT with a single one-time key." width="1280" height="800" >}}
    </div>
  </section>

  <section class="lm-showcase-row">
    <div class="lm-showcase-copy">
      <div class="lm-showcase-eyebrow">CLI &amp; Agent Skills</div>
      <h2 class="lm-showcase-title">Script your fleet from the CLI</h2>
      <p class="lm-showcase-text">Every workspace action is scriptable through the <code>leapmux</code> CLI (short alias <code>lmx</code>) and six built-in Agent Skills. Spawn worktree agents, wait without polling, enforce JSON schemas on replies, and forward TCP ports over encrypted channels.</p>
      <ul class="lm-showcase-list">
        <li>Wait on agents with <code>--wait</code> and <code>--any</code></li>
        <li>Enforce JSON schemas on agent replies</li>
        <li>Resume event streams and retry idempotently</li>
        <li>Six built-in skills for parallel workflows</li>
      </ul>
      <a class="lm-showcase-link" href="/docs/using/cli-guide/">Explore the CLI guide &rarr;</a>
    </div>
    <div class="lm-showcase-media">
      {{< cli-cast src="cli-fleet-demo" title="zsh — payments-api" cols="100" rows="18" caption="Live leapmux CLI capture: open Claude Code and Codex on isolated git worktrees, list active agents, dispatch tasks in parallel, wait for the first completion, and read its reply." >}}
    </div>
  </section>
</div>

{{< changelog-strip count="3" >}}

<section class="lm-home-faq" aria-labelledby="lm-home-faq-heading">
  <div class="lm-home-faq-header">
    <div class="lm-showcase-eyebrow">FAQ</div>
    <h2 id="lm-home-faq-heading" class="lm-home-faq-title">Common questions</h2>
  </div>
  <div class="lm-home-faq-list">
    <details class="lm-home-faq-item">
      <summary>Do I need separate API keys or a LeapMux subscription?</summary>
      <p>No. Bring your existing coding-agent CLI subscriptions or API keys across all {{< agent-provider-count >}} supported providers. LeapMux drives the agent CLIs you already authenticate on your laptop or remote Workers.</p>
    </details>
    <details class="lm-home-faq-item">
      <summary>Can the Hub read my code, chats, or terminal output?</summary>
      <p>No. Clients and Workers establish hybrid post-quantum end-to-end encrypted (E2EE) channels directly. The Hub relays ciphertext only and cannot read agent chats, terminal output, or files.</p>
    </details>
    <details class="lm-home-faq-item">
      <summary>Do my remote devboxes need inbound firewall ports open?</summary>
      <p>No. Workers dial out to the Hub over HTTPS and WebSockets, so remote servers, cloud VMs, and GPU boxes stay behind NAT with zero inbound firewall ports open.</p>
    </details>
    <details class="lm-home-faq-item">
      <summary>What is the difference between Solo Desktop, Solo Daemon, and Distributed mode?</summary>
      <p>Solo Desktop runs a local Hub and Worker inside the native app with one click, while Solo Daemon runs the same single-machine setup headlessly for browser and CLI access. Distributed mode pairs a central Hub with one or more remote Workers for multi-machine or team setups — see <a href="/docs/getting-started/concepts/">Concepts &amp; Topologies</a>.</p>
    </details>
    <details class="lm-home-faq-item">
      <summary>What license does LeapMux use?</summary>
      <p>LeapMux is source-available under the Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2), which converts to Apache 2.0 two years after each release. See <a href="/docs/reference/legal/">Legal</a> for full terms.</p>
    </details>
  </div>
</section>

<section class="lm-closing-cta">
  <h2>Ready to run your fleet?</h2>
  <p>Download LeapMux Desktop or start solo mode from the command line in under five minutes.</p>
  <div class="lm-download-hero-actions">
    <a class="lm-btn-primary" href="/download/">Download LeapMux</a>
    <a class="lm-btn-secondary" href="/docs/getting-started/quick-start/">5-Minute Quick Start</a>
    <a class="lm-btn-secondary" href="https://github.com/leapmux/leapmux/releases">Latest Releases on GitHub</a>
  </div>
</section>
