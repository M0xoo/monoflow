/**
 * MonoFlow site: theme toggle, copy buttons, output tabs, command filter.
 */

document.addEventListener('DOMContentLoaded', () => {
  initTheme();
  initCopy();
  initTabs();
  initFilter();
});

function initTheme() {
  const root = document.documentElement;
  try {
    const saved = localStorage.getItem('monoflow-theme');
    if (saved) root.dataset.theme = saved;
  } catch (_) { /* storage unavailable */ }

  const btn = document.getElementById('theme-toggle');
  if (!btn) return;
  btn.addEventListener('click', () => {
    const current = root.dataset.theme ||
      (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
    const next = current === 'dark' ? 'light' : 'dark';
    root.dataset.theme = next;
    try { localStorage.setItem('monoflow-theme', next); } catch (_) { /* ignore */ }
  });
}

function initCopy() {
  document.querySelectorAll('[data-copy]').forEach(btn => {
    btn.addEventListener('click', async () => {
      const el = document.getElementById(btn.dataset.copy);
      if (!el) return;
      try {
        await navigator.clipboard.writeText(el.innerText.trim());
        btn.textContent = 'Copied';
        setTimeout(() => { btn.textContent = 'Copy'; }, 1500);
      } catch (err) {
        console.error('Copy failed:', err);
      }
    });
  });
}

function initTabs() {
  document.querySelectorAll('[data-tabs]').forEach(group => {
    const tabs = group.querySelectorAll('[role="tab"]');
    const panels = group.querySelectorAll('[data-panel]');
    tabs.forEach(tab => {
      tab.addEventListener('click', () => {
        tabs.forEach(t => t.setAttribute('aria-selected', String(t === tab)));
        panels.forEach(p => { p.hidden = p.dataset.panel !== tab.dataset.tab; });
      });
    });
  });
}

function initFilter() {
  const input = document.getElementById('cmd-filter');
  const table = document.getElementById('cmd-table');
  if (!input || !table) return;

  input.addEventListener('input', () => {
    const q = input.value.trim().toLowerCase();
    let group = null;
    let groupHasMatch = false;
    const finishGroup = () => { if (group) group.hidden = q !== '' && !groupHasMatch; };

    table.querySelectorAll('tbody tr').forEach(row => {
      if (row.classList.contains('grp')) {
        finishGroup();
        group = row;
        groupHasMatch = false;
        return;
      }
      const match = q === '' || row.textContent.toLowerCase().includes(q);
      row.hidden = !match;
      if (match) groupHasMatch = true;
    });
    finishGroup();
  });
}
