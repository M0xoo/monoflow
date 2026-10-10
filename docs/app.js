document.addEventListener('DOMContentLoaded', () => {
  // Theme Toggle
  const themeToggleBtn = document.getElementById('theme-toggle');
  const currentTheme = localStorage.getItem('monoflow-theme') || 'dark';
  document.documentElement.setAttribute('data-theme', currentTheme);
  updateThemeIcon(currentTheme);

  if (themeToggleBtn) {
    themeToggleBtn.addEventListener('click', () => {
      const theme = document.documentElement.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
      document.documentElement.setAttribute('data-theme', theme);
      localStorage.setItem('monoflow-theme', theme);
      updateThemeIcon(theme);
    });
  }

  function updateThemeIcon(theme) {
    if (themeToggleBtn) {
      themeToggleBtn.textContent = theme === 'dark' ? '☀️' : '🌙';
    }
  }

  // Mobile Menu Toggle
  const mobileMenuBtn = document.getElementById('mobile-menu-btn');
  const sidebar = document.querySelector('.sidebar');
  if (mobileMenuBtn && sidebar) {
    mobileMenuBtn.addEventListener('click', () => {
      sidebar.classList.toggle('open');
    });
  }

  // Copy Buttons
  document.querySelectorAll('.copy-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const targetId = btn.getAttribute('data-target');
      let textToCopy = '';
      if (targetId) {
        const targetEl = document.getElementById(targetId);
        textToCopy = targetEl ? targetEl.textContent : '';
      } else {
        const codeEl = btn.closest('.code-container')?.querySelector('code');
        textToCopy = codeEl ? codeEl.textContent : btn.previousElementSibling?.textContent || '';
      }

      if (textToCopy) {
        navigator.clipboard.writeText(textToCopy.trim()).then(() => {
          const originalText = btn.innerHTML;
          btn.innerHTML = '✓ Copied!';
          setTimeout(() => {
            btn.innerHTML = originalText;
          }, 2000);
        });
      }
    });
  });

  // Search Filter
  const searchInput = document.getElementById('search-input');
  if (searchInput) {
    searchInput.addEventListener('input', (e) => {
      const query = e.target.value.toLowerCase().trim();
      const sidebarLinks = document.querySelectorAll('.sidebar-link');

      sidebarLinks.forEach(link => {
        const text = link.textContent.toLowerCase();
        if (text.includes(query) || !query) {
          link.style.display = 'flex';
        } else {
          link.style.display = 'none';
        }
      });
    });

    window.addEventListener('keydown', (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault();
        searchInput.focus();
      }
    });
  }

  // Scroll Spy for Sidebar Active Link
  const sections = document.querySelectorAll('section[id]');
  window.addEventListener('scroll', () => {
    let current = '';
    sections.forEach(section => {
      const sectionTop = section.offsetTop - 120;
      if (window.pageYOffset >= sectionTop) {
        current = section.getAttribute('id');
      }
    });

    document.querySelectorAll('.sidebar-link').forEach(link => {
      link.classList.remove('active');
      if (link.getAttribute('href') === `#${current}`) {
        link.classList.add('active');
      }
    });
  });

  // Interactive HUD Simulator
  const simOutput = document.getElementById('sim-output');
  const simTabs = document.querySelectorAll('.hud-tab');

  const scenarios = {
    ast: {
      cmd: "monoflow worktree sync --to=agent-alpha --from=agent-beta",
      json: `{
  "status": "synced",
  "from_agent": "agent-beta",
  "to_agent": "agent-alpha",
  "synced_files": [
    "packages/shared-go/logger/logger.go (synthesized AST)"
  ],
  "duration_ms": 25.766,
  "verdict": "Semantic AST synthesis united both function declarations with 0 conflict markers."
}`
    },
    slop: {
      cmd: 'monoflow review audit --title="docs: fix typo" --desc="minor doc update"',
      json: `{
  "valid": false,
  "undeclared_files": [
    "packages/database-schemas/migrations/003_stripe_customer.sql"
  ],
  "hallucination_warnings": [
    "PR claims trivial change ('docs: fix typo') but modifies non-doc file: jwt.go",
    "Undisclosed critical blast radius: 003_stripe_customer.sql modified in Git but omitted from PR description"
  ],
  "verdict": "PR intent audit failed: unmentioned critical files or misleading claims detected"
}`
    },
    queue: {
      cmd: "monoflow queue spec task-enterprise-007",
      json: `{
  "task_id": "task-enterprise-007",
  "speculative_merge_sha": "37bc26d587aa3e51e456a51125ddfc54a7b9bbced5d1ab8587aba299fbfbd84c",
  "affected_packages": ["services/billing-service"],
  "bypassed_quarantined_tests": [
    "test_stripe_webhook_timeout"
  ],
  "duration_ms": 36.729,
  "status": "speculatively_verified",
  "signature": "32ccd419d1817687bfa0083ce879cd0b6c5bcd2692e2227a27abe56ea3d148e1",
  "verdict": "Eligible for GitHub Actions Tier 1 Fast-Path Merge (< 30s)"
}`
    },
    circuit: {
      cmd: "monoflow verify --affected (Attempt 3/3)",
      json: `{
  "circuit_breaker": "TRIPPED",
  "attempts": 3,
  "max_attempts": 3,
  "action": "Auto-reverted working tree to latest green checkpoint to prevent hallucination loop",
  "escalate_to_human": true,
  "reason": "Target 'services/billing-service' failed 3 consecutive times without passing"
}`
    },
    proof: {
      cmd: "monoflow verify --proof=task-billing-discount",
      json: `{
  "status": "VERIFIED",
  "task_id": "task-billing-discount",
  "verified_in_ms": 0.159,
  "dod_checks_passed": 4,
  "git_tree_sha": "2e90bad1e5030e3e0d10f6a314eaa2946ff487da",
  "deliverable_sha": "8221286bcc8982acc0dd5827536a32d6a63e5a19cf6782c55118371344c42f0e",
  "confidence_verdict": "APPROVED (1-Second Instant Verification Passed)"
}`
    }
  };

  simTabs.forEach(tab => {
    tab.addEventListener('click', () => {
      simTabs.forEach(t => t.classList.remove('active'));
      tab.classList.add('active');
      const scenarioKey = tab.getAttribute('data-scenario');
      const data = scenarios[scenarioKey];
      if (data && simOutput) {
        simOutput.innerHTML = `<span style="color: var(--neon-cyan)">$ ${data.cmd}</span>\n\n${syntaxHighlightJSON(data.json)}`;
      }
    });
  });

  function syntaxHighlightJSON(jsonStr) {
    return jsonStr.replace(/("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?)/g, match => {
      let cls = 'color: #00f0ff;';
      if (/^"/.test(match)) {
        if (/:$/.test(match)) {
          cls = 'color: #93c5fd; font-weight: 600;';
        } else {
          cls = 'color: #00ff88;';
        }
      } else if (/true|false/.test(match)) {
        cls = 'color: #ffb703;';
      } else if (/null/.test(match)) {
        cls = 'color: #ff3366;';
      }
      return `<span style="${cls}">${match}</span>`;
    });
  }

  // Default trigger
  const defaultTab = document.querySelector('.hud-tab[data-scenario="ast"]');
  if (defaultTab) {
    defaultTab.click();
  }

  // Interactive CI Latency & Cost Calculator
  const pkgInput = document.getElementById('calc-pkg-count');
  const ciTimeOutput = document.getElementById('calc-ci-time');
  const mfTimeOutput = document.getElementById('calc-mf-time');
  const speedupOutput = document.getElementById('calc-speedup');
  const savingsOutput = document.getElementById('calc-savings');

  if (pkgInput) {
    const updateCalculator = () => {
      const count = Math.max(2, parseInt(pkgInput.value || 12, 10));
      const tradTimeMins = Math.round(count * 2.8);
      const mfTimeSecs = (0.2 + (count * 0.12)).toFixed(1);
      const speedup = Math.round((tradTimeMins * 60) / mfTimeSecs);
      const monthlySavings = Math.round(count * 115);

      if (ciTimeOutput) ciTimeOutput.textContent = `${tradTimeMins} mins`;
      if (mfTimeOutput) mfTimeOutput.textContent = `${mfTimeSecs}s`;
      if (speedupOutput) speedupOutput.textContent = `${speedup}x`;
      if (savingsOutput) savingsOutput.textContent = `$${monthlySavings.toLocaleString()}/mo`;
    };

    pkgInput.addEventListener('input', updateCalculator);
    updateCalculator();
  }
});
