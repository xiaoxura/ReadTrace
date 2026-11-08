import { api } from '../api.js';
import { state, refreshAccounts } from '../state.js';
import { icon } from '../icons.js';
import { runDetailDrawer, startRunDialog } from '../components.js';
import {
  renderError, skeletonHTML, runRowHTML, bindRunRows,
  bindStopButtons, bindRunStateRefresh,
} from '../helpers.js';
import { $, $$, escapeHtml, fmtMinutes, pageHeaderHTML } from '../util.js';

export async function renderRuns(ctx) {
  const { root } = ctx;
  root.innerHTML = skeletonHTML(['height:70px;width:45%', 'height:300px']);

  if (!state.accounts.length) {
    try { await refreshAccounts(); } catch { /* ignore */ }
  }
  if (!ctx.isActive()) return;
  const saved = Number(sessionStorage.getItem('wxread-runs-account') || 0);
  const accountId = state.accounts.some((a) => a.id === saved) ? saved : 0;

  let data;
  try {
    data = await api.runs(accountId, 120);
  } catch (e) {
    renderError(root, e, { onRetry: () => ctx.reload() });
    return;
  }
  if (!ctx.isActive()) return;

  const active = data.active || [];
  const rows = (data.runs || []).map((r) => runRowHTML(r, { endTime: true })).join('');

  root.innerHTML = `
    ${pageHeaderHTML({
      title: '阅读记录',
      sub: `最近 ${(data.runs || []).length} 条`,
      actions: `
        <button class="btn btn-ghost" id="btn-refresh">${icon('refresh')}<span>刷新</span></button>
        <button class="btn btn-primary" id="btn-start">${icon('play')}<span>开始阅读</span></button>`,
    })}

    <div class="tool-row">
      <select class="select" id="run-filter" style="width:auto;min-width:120px" aria-label="按账号筛选">
        <option value="0" ${!accountId ? 'selected' : ''}>全部账号</option>
        ${state.accounts.map((a) => `<option value="${a.id}" ${a.id === accountId ? 'selected' : ''}>${escapeHtml(a.name)}</option>`).join('')}
      </select>
      <input class="input" id="run-search" placeholder="搜索账号 / 状态 / 触发" style="min-width:200px">
    </div>

    ${active.length ? `<div class="card" style="margin-bottom:16px">
      <div class="card-head"><div><div class="card-title">${icon('wifi')}正在运行</div><div class="card-sub">点击查看实时日志</div></div></div>
      <div class="list">
        ${active.map((p) => {
          const pct = p.target ? Math.min(100, Math.round((p.elapsed / p.target) * 100)) : 0;
          return `<div class="list-row">
            <div class="schedule-icon on">${icon('play')}</div>
            <div class="list-main">
              <div class="list-title">${escapeHtml(p.account_name)} <span class="badge run"><span class="pulse-dot"></span>${pct}%</span></div>
              <div class="list-sub">已读 ${fmtMinutes(p.elapsed)} / ${fmtMinutes(p.target)} 分钟 · 成功 ${escapeHtml(p.success)} 次</div>
            </div>
            <button class="btn btn-sm" data-open="${p.run_id}">查看</button>
            <button class="btn btn-sm btn-danger" data-stop="${p.run_id}">停止</button>
          </div>`;
        }).join('')}
      </div>
    </div>` : ''}

    <article class="card">
      <div class="card-head">
        <div><div class="card-title">${icon('list')}全部记录</div><div class="card-sub">点击任意一行查看日志</div></div>
      </div>
      ${rows ? `<div class="table-wrap"><table class="table table-wide">
        <thead><tr><th>状态</th><th>账号</th><th>触发</th><th>时长</th><th>进度</th><th>开始时间</th><th>结束时间</th><th></th></tr></thead>
        <tbody data-run-rows>${rows}</tbody>
      </table></div>` : `<div class="empty" style="padding:44px 10px">
        <div class="empty-ico">${icon('list')}</div><h3>还没有阅读记录</h3>
        <p>点击右上角「开始阅读」，或创建一个定时任务。</p>
      </div>`}
    </article>
  `;
  if (!ctx.isActive()) return;

  $('#run-filter', root)?.addEventListener('change', (e) => {
    sessionStorage.setItem('wxread-runs-account', e.target.value);
    ctx.reload();
  });
  $('#btn-refresh', root)?.addEventListener('click', () => ctx.reload());
  $('#btn-start', root)?.addEventListener('click', () => {
    startRunDialog({ accountId, onDone: () => ctx.reload() });
  });
  $('#run-search', root)?.addEventListener('input', (e) => {
    const q = e.target.value.trim().toLowerCase();
    $$('[data-run-rows] tr', root).forEach((tr) => {
      tr.style.display = !q || tr.textContent.toLowerCase().includes(q) ? '' : 'none';
    });
  });
  bindRunRows(root, (id) => runDetailDrawer(id, { onDone: () => ctx.reload() }));
  $$('[data-open]', root).forEach((btn) => btn.addEventListener('click', () => {
    runDetailDrawer(Number(btn.dataset.open), { onDone: () => ctx.reload() });
  }));
  bindStopButtons(root, { onDone: () => ctx.reload() });

  ctx.setHandler(bindRunStateRefresh(ctx));
}
