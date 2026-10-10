/**
 * MonoFlow Interactive Systems Workbench & Playground (2026)
 */

document.addEventListener('DOMContentLoaded', () => {
  initClipboard();
  initWorkbench();
  initCalculator();
  initTheme();
  initSearch();
});

// ============================================================================
// Clipboard Handler
// ============================================================================
function initClipboard() {
  document.querySelectorAll('.btn-copy').forEach(btn => {
    btn.addEventListener('click', async (e) => {
      e.preventDefault();
      const targetId = btn.getAttribute('data-target');
      let textToCopy = '';
      
      if (targetId) {
        const el = document.getElementById(targetId);
        if (el) textToCopy = el.innerText.trim();
      } else {
        const codeBlock = btn.closest('.code-container, .command-strip, .install-bar')?.querySelector('code, .install-cmd');
        if (codeBlock) textToCopy = codeBlock.innerText.trim();
      }

      if (!textToCopy) return;

      try {
        await navigator.clipboard.writeText(textToCopy);
        const originalText = btn.innerText;
        btn.innerText = 'COPIED ✓';
        btn.classList.add('copied');
        setTimeout(() => {
          btn.innerText = originalText;
          btn.classList.remove('copied');
        }, 1800);
      } catch (err) {
        console.error('Copy failed:', err);
      }
    });
  });
}

// ============================================================================
// Interactive Workbench
// ============================================================================
const workbenchContent = {
  cli: `
<div class="cli-preset-bar">
  <span class="cli-label">Select Command:</span>
  <button class="cli-btn" onclick="runCliCmd('synthesize')">$ monoflow synthesize</button>
  <button class="cli-btn" onclick="runCliCmd('checkpoint')">$ monoflow checkpoint</button>
  <button class="cli-btn" onclick="runCliCmd('worktree')">$ monoflow worktree spawn</button>
  <button class="cli-btn" onclick="runCliCmd('verify')">$ monoflow verify</button>
  <button class="cli-btn" onclick="runCliCmd('receipt')">$ monoflow receipt sign</button>
</div>
<div id="terminal-stream">
<span class="c-amber c-bold">$ monoflow synthesize pkg/billing/service.go</span>
<span class="c-muted">[00:00:00.001]</span> <span class="c-cyan">PARSER</span> Loading Go AST trees for base (main), agent-1 (feat/ratelimit), agent-2 (feat/metrics)
<span class="c-muted">[00:00:00.008]</span> <span class="c-cyan">AST-DIFF</span> agent-1 added 1 import ("golang.org/x/time/rate"), 1 struct field (*rate.Limiter), 1 method (CheckQuota)
<span class="c-muted">[00:00:00.014]</span> <span class="c-cyan">AST-DIFF</span> agent-2 added 1 import ("github.com/prometheus/client_golang/prometheus"), 1 package var (chargeCounter)
<span class="c-muted">[00:00:00.019]</span> <span class="c-emerald">SYNTHESIZER</span> AST graph nodes disjoint; merging AST declarations without textual conflict
<span class="c-muted">[00:00:00.024]</span> <span class="c-emerald">SYNTHESIZER</span> Validating Go type checking and compilation signatures... OK
<span class="c-muted">[00:00:00.025]</span> <span class="c-amber">PLUMBING</span> Ephemeral tree hash written: <span class="c-purple">9fa84b1e7c902d33458fa...</span>
<span class="c-muted">[00:00:00.025]</span> <span class="c-emerald c-bold">SUCCESS</span> Synthesis complete in 24.8ms. Zero merge conflicts.
</div>`,

  ast: `
<span class="c-cyan">// === AST 3-Way Semantic Synthesis Inspector ===</span>
<span class="c-muted">// Target File:</span> pkg/billing/service.go
<span class="c-muted">// Status:</span> <span class="c-emerald">Zero git conflict. Synthesized automatically via Go AST grafting in 24.2ms.</span>

<span class="c-purple">package</span> billing

<span class="c-purple">import</span> (
  <span class="c-emerald">"github.com/prometheus/client_golang/prometheus"</span>  <span class="c-muted">// Grafted from Agent-2 (Metrics)</span>
  <span class="c-cyan">"golang.org/x/time/rate"</span>                           <span class="c-muted">// Grafted from Agent-1 (RateLimiter)</span>
  <span class="c-muted">"sync"</span>
)

<span class="c-purple">type</span> BillingService <span class="c-purple">struct</span> {
  mu      sync.RWMutex
  <span class="c-cyan">limiter *rate.Limiter</span>                               <span class="c-muted">// Added by Agent-1 (AST Node #428)</span>
}

<span class="c-muted">// Added by Agent-2 (AST Node #439)</span>
<span class="c-purple">var</span> <span class="c-emerald">chargeCounter</span> = prometheus.NewCounter(prometheus.CounterOpts{
  Name: "billing_charges_total",
})

<span class="c-purple">func</span> (s *BillingService) ProcessCharge(ctx context.Context, amount int64) error {
  <span class="c-cyan">if !s.limiter.Allow() {</span>                            <span class="c-muted">// Injected AST block (Agent-1)</span>
    <span class="c-rose">return ErrRateLimited</span>
  }
  <span class="c-emerald">chargeCounter.Inc()</span>                                <span class="c-muted">// Injected AST block (Agent-2)</span>
  <span class="c-muted">return s.executeTransaction(ctx, amount)</span>
}`,

  cow: `
<span class="c-cyan">// === APFS / btrfs Copy-on-Write Worktree Inspector ===</span>
<span class="c-muted">Command:</span> monoflow worktree spawn --agent worker-3 --share-caches

<span class="c-muted">[00:00:00.002]</span> <span class="c-cyan">VFS-DETECT</span> Host filesystem: Darwin APFS (supports clonefile() zero-copy CoW)
<span class="c-muted">[00:00:00.018]</span> <span class="c-cyan">CLONEFILE</span> Cloning repository tree to .agents/worktrees/worker-3
<span class="c-muted">[00:00:00.035]</span> <span class="c-amber">COW-CACHE</span> Cloning node_modules (420MB) -> Disk allocated: <span class="c-emerald">0 bytes</span> (shared physical blocks)
<span class="c-muted">[00:00:00.048]</span> <span class="c-amber">COW-CACHE</span> Cloning .venv (180MB)        -> Disk allocated: <span class="c-emerald">0 bytes</span> (shared physical blocks)
<span class="c-muted">[00:00:00.054]</span> <span class="c-cyan">GIT-ATTACH</span> Attaching worktree HEAD to ephemeral shadow branch agent/worker-3
<span class="c-muted">[00:00:00.058]</span> <span class="c-emerald c-bold">READY</span> Virtual worktree worker-3 provisioned in <span class="c-emerald">58.4ms</span>.
<span class="c-muted">Active Worktrees:</span>
  • worker-1: branch=feat/auth-jwt  lease=5m42s   dirty_files=2   cow_blocks=14MB
  • worker-2: branch=feat/db-pool   lease=8m10s   dirty_files=4   cow_blocks=28MB
  • worker-3: branch=feat/billing   lease=10m00s  dirty_files=0   cow_blocks=0MB`,

  audit: `
<span class="c-rose">// === Agentic Slop & Scope Auditor Diagnostic ===</span>
<span class="c-muted">PR:</span> #142 (Author: autonomous-agent-claude-3.7)
<span class="c-muted">Intent Target:</span> "Add exponential backoff retry to payment webhook client"

<span class="c-cyan">AUDIT RESULTS:</span>
  [✓] <span class="c-emerald">INTENT RELEVANCE SCORE: 94.2%</span> (pkg/payment/webhook.go matches declared intent)
  [✓] <span class="c-emerald">TEST INTEGRITY CHECK: PASS</span> (0 test assertions altered or deleted)
  [!] <span class="c-rose c-bold">HALLUCINATION DETECTED:</span>
      File: pkg/payment/types.go (Line 184)
      Diff: Replaced 14 lines with unprompted refactoring of unrelated CustomerID schema.
      <span class="c-rose">Action: Auto-rejected diff chunk #3; quarantined to sandbox.</span>
  [✓] <span class="c-emerald">PROMPT INJECTION SCAN: CLEAN</span> (No system prompt bypass attempts in docstrings)

<span class="c-muted">Gate Status:</span> <span class="c-amber">AUTO-TRIMMED</span> (Discarded hallucinated chunk, accepted focused webhook patch).`,

  queue: `
<span class="c-cyan">// === Speculative DAG Merge Queue ===</span>
<span class="c-muted">Base Commit:</span> origin/main (SHA: 41b8a902)
<span class="c-muted">Active Parallel PRs in Speculative Flight:</span> 4

<span class="c-muted">TOPOLOGY DAG:</span>
  ├── <span class="c-emerald">PR #204 (pkg/auth)</span>      ──> Affected: [pkg/auth]            ──> Status: <span class="c-emerald">SPECULATIVE PASS (Merged ahead of CI)</span>
  ├── <span class="c-emerald">PR #205 (pkg/analytics)</span> ──> Affected: [pkg/analytics]       ──> Status: <span class="c-emerald">SPECULATIVE PASS (Disjoint DAG node)</span>
  ├── <span class="c-amber">PR #206 (pkg/api)</span>       ──> Affected: [pkg/api, pkg/auth]   ──> Status: <span class="c-amber">AUTO-REBASING ON PR #204 (< 40ms)</span>
  └── <span class="c-cyan">PR #207 (pkg/database)</span>  ──> Affected: [pkg/database]        ──> Status: <span class="c-cyan">AWAITING 1S POW RECEIPT</span>

<span class="c-muted">Merge Queue Throughput:</span> <span class="c-emerald">4 PRs processed in 4.2 seconds</span> (Traditional CI would take 52 minutes).`,

  pow: `
<span class="c-cyan">// === 1-Second Reviewer Proof-of-Work Receipt (.monoflow/proofs/pr-204.json) ===</span>
{
  <span class="c-amber">"version"</span>: "1.0",
  <span class="c-amber">"pr_number"</span>: 204,
  <span class="c-amber">"base_tree_sha"</span>: <span class="c-purple">"41b8a902d847c1b50ef91a7834bc2174"</span>,
  <span class="c-amber">"head_tree_sha"</span>: <span class="c-purple">"9fa84b1e7c902d33458fa6c2b1897e01"</span>,
  <span class="c-amber">"affected_packages"</span>: [<span class="c-emerald">"pkg/auth"</span>, <span class="c-emerald">"pkg/auth/jwt"</span>],
  <span class="c-amber">"execution_telemetry"</span>: {
    <span class="c-cyan">"ast_synthesis_ms"</span>: 24.8,
    <span class="c-cyan">"test_execution_ms"</span>: 1140.2,
    <span class="c-cyan">"tests_passed"</span>: 48,
    <span class="c-cyan">"tests_quarantined"</span>: 0
  },
  <span class="c-amber">"reviewer_verification"</span>: {
    <span class="c-cyan">"verifier"</span>: "monoflow-pow-engine",
    <span class="c-cyan">"sha256_tree_signature"</span>: <span class="c-purple">"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"</span>,
    <span class="c-cyan">"verification_cost"</span>: <span class="c-emerald">"0.159ms"</span>,
    <span class="c-cyan">"gate_verdict"</span>: <span class="c-emerald c-bold">"PROVEN_PASS"</span>
  }
}`
};

function initWorkbench() {
  const tabs = document.querySelectorAll('.wb-tab');
  const viewport = document.getElementById('workbench-viewport');
  if (!viewport) return;

  // Set initial content
  viewport.innerHTML = workbenchContent.cli;

  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      tabs.forEach(t => t.classList.remove('active'));
      tab.classList.add('active');
      const target = tab.getAttribute('data-target');
      if (workbenchContent[target]) {
        viewport.innerHTML = workbenchContent[target];
      }
    });
  });
}

// Global CLI command simulator for workbench
window.runCliCmd = function(cmd) {
  const stream = document.getElementById('terminal-stream');
  if (!stream) return;

  const commands = {
    synthesize: `
<span class="c-amber c-bold">$ monoflow synthesize pkg/billing/service.go</span>
<span class="c-muted">[00:00:00.001]</span> <span class="c-cyan">PARSER</span> Loading Go AST trees for base (main), agent-1 (feat/ratelimit), agent-2 (feat/metrics)
<span class="c-muted">[00:00:00.008]</span> <span class="c-cyan">AST-DIFF</span> agent-1 added 1 import, 1 struct field, 1 method
<span class="c-muted">[00:00:00.014]</span> <span class="c-cyan">AST-DIFF</span> agent-2 added 1 import, 1 package var
<span class="c-muted">[00:00:00.019]</span> <span class="c-emerald">SYNTHESIZER</span> AST graph nodes disjoint; merging AST declarations without textual conflict
<span class="c-muted">[00:00:00.024]</span> <span class="c-emerald">SYNTHESIZER</span> Validating Go type checking signatures... OK
<span class="c-muted">[00:00:00.025]</span> <span class="c-emerald c-bold">SUCCESS</span> Synthesis complete in 24.8ms. Zero merge conflicts.`,

    checkpoint: `
<span class="c-amber c-bold">$ monoflow checkpoint</span>
<span class="c-muted">[00:00:00.002]</span> <span class="c-cyan">PLUMBING</span> Invoking git write-tree on shadow index...
<span class="c-muted">[00:00:00.011]</span> <span class="c-cyan">TREE-SHA</span> Created tree object: 7f83b1657ff1fc53b92dc18148a1d65dfc2d4b1f
<span class="c-muted">[00:00:00.014]</span> <span class="c-emerald c-bold">CHECKPOINT SAVED</span> in 14.2ms. Workdir clean; zero index pollution.`,

    worktree: `
<span class="c-amber c-bold">$ monoflow worktree spawn --agent worker-4 --share-caches</span>
<span class="c-muted">[00:00:00.003]</span> <span class="c-cyan">VFS-DETECT</span> Apple APFS clonefile() supported
<span class="c-muted">[00:00:00.021]</span> <span class="c-cyan">CLONEFILE</span> CoW clone of node_modules and .venv completed (0 bytes copied)
<span class="c-muted">[00:00:00.058]</span> <span class="c-emerald c-bold">READY</span> Virtual worktree worker-4 spawned in 58.1ms at .agents/worktrees/worker-4`,

    verify: `
<span class="c-amber c-bold">$ monoflow verify --scope affected</span>
<span class="c-muted">[00:00:00.005]</span> <span class="c-cyan">DAG-GRAPH</span> Computed affected boundary: [pkg/auth, pkg/gateway]
<span class="c-muted">[00:00:00.012]</span> <span class="c-cyan">TEST-RUNNER</span> Executing tests in parallel for affected packages...
<span class="c-muted">[00:01:00.180]</span> <span class="c-emerald">PASS</span> pkg/auth (24 tests passed in 680ms)
<span class="c-muted">[00:01:00.210]</span> <span class="c-emerald">PASS</span> pkg/gateway (18 tests passed in 490ms)
<span class="c-muted">[00:01:00.215]</span> <span class="c-emerald c-bold">ALL CHECKS PASSED</span> in 1.18s total wall time.`,

    receipt: `
<span class="c-amber c-bold">$ monoflow receipt sign --pr 208</span>
<span class="c-muted">[00:00:00.001]</span> <span class="c-cyan">CRYPTO</span> Signing affected package test digests with repository key...
<span class="c-muted">[00:00:00.003]</span> <span class="c-emerald">RECEIPT GENERATED</span> Written to .monoflow/proofs/pr-208.json
<span class="c-muted">[00:00:00.004]</span> <span class="c-emerald c-bold">VERIFICATION PROOF:</span> SHA-256 e3b0c44298fc1c14... Reviewers verify in 0.159ms.`
  };

  if (commands[cmd]) {
    stream.innerHTML = commands[cmd];
  }
};

// ============================================================================
// ROI Calculator
// ============================================================================
function initCalculator() {
  const agentsSlider = document.getElementById('calc-agents');
  const prsSlider = document.getElementById('calc-prs');
  const hoursSavedEl = document.getElementById('calc-hours-saved');
  const costSavedEl = document.getElementById('calc-cost-saved');
  const agentsValEl = document.getElementById('calc-agents-val');
  const prsValEl = document.getElementById('calc-prs-val');

  if (!agentsSlider || !prsSlider) return;

  function update() {
    const agents = parseInt(agentsSlider.value, 10);
    const prs = parseInt(prsSlider.value, 10);

    if (agentsValEl) agentsValEl.innerText = agents;
    if (prsValEl) prsValEl.innerText = prs;

    // Traditional CI: 25 mins per PR.
    // MonoFlow CI: 1.5 mins per PR.
    // Time saved per PR: 23.5 mins = 0.391 hours.
    // Monthly PRs: prs * 22 working days.
    const monthlyPRs = prs * 22;
    const hoursSaved = Math.round(monthlyPRs * 0.391);
    
    // Cloud Runner cost ($0.03/min) + Agent token/compute stall costs ($15/hr)
    const costSaved = Math.round(hoursSaved * 42);

    if (hoursSavedEl) hoursSavedEl.innerText = hoursSaved.toLocaleString() + ' hrs';
    if (costSavedEl) costSavedEl.innerText = '$' + costSaved.toLocaleString();
  }

  agentsSlider.addEventListener('input', update);
  prsSlider.addEventListener('input', update);
  update();
}

// ============================================================================
// Theme Switcher
// ============================================================================
function initTheme() {
  const toggle = document.getElementById('theme-toggle');
  if (!toggle) return;

  const currentTheme = localStorage.getItem('monoflow_theme') || 'dark';
  document.documentElement.setAttribute('data-theme', currentTheme);
  toggle.innerText = currentTheme === 'dark' ? '☀' : '☾';

  toggle.addEventListener('click', () => {
    const theme = document.documentElement.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', theme);
    localStorage.setItem('monoflow_theme', theme);
    toggle.innerText = theme === 'dark' ? '☀' : '☾';
  });
}

// ============================================================================
// Command Palette Search (⌘K)
// ============================================================================
function initSearch() {
  window.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
      e.preventDefault();
      const searchTrigger = document.querySelector('.search-trigger');
      if (searchTrigger) searchTrigger.click();
    }
  });

  const searchTrigger = document.querySelector('.search-trigger');
  if (searchTrigger) {
    searchTrigger.addEventListener('click', () => {
      const query = prompt('Quick jump: Type a topic (e.g., ast, cow, checkpoints, cli, pow, benchmarks):');
      if (!query) return;
      const q = query.toLowerCase();
      if (q.includes('ast') || q.includes('synth')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const astTab = document.querySelector('[data-target="ast"]');
        if (astTab) astTab.click();
      } else if (q.includes('cow') || q.includes('worktree')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const cowTab = document.querySelector('[data-target="cow"]');
        if (cowTab) cowTab.click();
      } else if (q.includes('bench') || q.includes('perf')) {
        document.getElementById('benchmarks')?.scrollIntoView({ behavior: 'smooth' });
      } else if (q.includes('cli') || q.includes('command')) {
        document.getElementById('cli')?.scrollIntoView({ behavior: 'smooth' });
      } else if (q.includes('pow') || q.includes('receipt') || q.includes('review')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const powTab = document.querySelector('[data-target="pow"]');
        if (powTab) powTab.click();
      } else {
        document.getElementById('architecture')?.scrollIntoView({ behavior: 'smooth' });
      }
    });
  }
}
