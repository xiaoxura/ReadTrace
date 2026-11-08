import { api } from '../api.js';
import { state, refreshAccounts } from '../state.js';
import { icon } from '../icons.js';
import { runDetailDrawer, startRunDialog } from '../components.js';
import {
  renderError, skeletonHTML, runRowHTML, bindRunRows,
  bindStopButtons, bindRunStateRefresh, pagerHTML, bindPager, pageSlice,
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
    data = await api.runs(accountId, 300);
  } catch (e) {
    renderError(root, e, { onRetry: () => ctx.reload() });
    return;
  }
  if (!ctx.isActive()) return;

  const active = data.active || [];

  root.innerHTML = `
    ${pageHeaderHTML({
      title: '阅读记录',
      sub: `最近 ${(data.runs || []).length} 条`,
      actions: `
        <button class="btn btn-ghost" id="btn-refresh">${icon('refresh')}<span>刷新</span></button>
        <button class="btn btn-primary" id="btn-start">${icon('play')}<span>开始阅读</span></button>`,
    })}

    <div class="tool-row">
      <select class="select" id="run-filter" style="width:auto;min-width:150px" aria-label="按账号筛选">
        <option value="0" ${!accountId ? 'selected' : ''}>全部账号</option>
        ${state.accounts.map((a) => `<option value="${a.id}" ${a.id === accountId ? 'selected' : ''}>${escapeHtml(a.name)}</option>`).join('')}
      </select>
      <select class="select" id="run-status" style="width:auto;min-width:120px" aria-label="按状态筛选">
        <option value="all">全部状态</option>
        <option value="running">进行中</option>
        <option value="success">成功</option>
        <option value="failed">失败</option>
        <option value="stopped">已停止</option>
      </select>
      <select class="select" id="run-trigger" style="width:auto;min-width:120px" aria-label="按触发方式筛选">
        <option value="all">全部触发</option>
        <option value="manual">手动</option>
        <option value="schedule">定时</option>
      </select>
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
        <div><div class="card-title">${icon('list')}全部记录</div><div class="card-sub">点击任意一行查看日志 · 支持按页浏览</div></div>
      </div>
      <div data-runs-table></div>
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

  const statusSel = $('#run-status', root);
  const triggerSel = $('#run-trigger', root);
  const statusFilter = sessionStorage.getItem('wxread-runs-status') || 'all';
  const triggerFilter = sessionStorage.getItem('wxread-runs-trigger') || 'all';
  if (statusSel) statusSel.value = [...statusSel.options].some((o) => o.value === statusFilter) ? statusFilter : 'all';
  if (triggerSel) triggerSel.value = [...triggerSel.options].some((o) => o.value === triggerFilter) ? triggerFilter : 'all';

  // 筛选 + 客户端分页：过滤后的子集按页渲染。
  const tableHost = $('[data-runs-table]', root);
  let page = Number(sessionStorage.getItem('wxread-runs-page') || 1) || 1;
  let pageSize = Number(sessionStorage.getItem('wxread-runs-size') || 20) || 20;

  const drawTable = () => {
    const runs = data.runs || [];
    const status = statusSel?.value || 'all';
    const trigger = triggerSel?.value || 'all';
    const filtered = runs.filter((r) =>
      (status === 'all' || r.status === status) && (trigger === 'all' || r.trigger === trigger));
    const total = filtered.length;
    const pages = Math.max(1, Math.ceil(total / pageSize));
    if (page > pages) page = pages;
    sessionStorage.setItem('wxread-runs-page', String(page));
    sessionStorage.setItem('wxread-runs-size', String(pageSize));

    if (!total) {
      tableHost.innerHTML = `<div class="empty" style="padding:44px 10px">
        <div class="empty-ico">${icon('list')}</div><h3>没有符合条件的记录</h3>
        <p>点击右上角「开始阅读」，或创建一个定时任务。</p>
      </div>`;
      return;
    }
    tableHost.innerHTML = `
      <div class="table-wrap"><table class="table runs-table table-wide">
        <thead><tr><th>状态</th><th>账号</th><th>触发</th><th>时长</th><th>进度</th><th>开始时间</th><th>结束时间</th><th></th></tr></thead>
        <tbody data-run-rows>${pageSlice(filtered, page, pageSize).map((r) => runRowHTML(r, { endTime: true })).join('')}</tbody>
      </table></div>
      ${pagerHTML(total, page, pageSize)}
    `;
    bindPager(tableHost, {
      onPage: (delta) => { page = Math.max(1, page + delta); drawTable(); },
      onSize: (size) => { pageSize = size; page = 1; drawTable(); },
    });
    bindRunRows(tableHost, (id) => runDetailDrawer(id, { onDone: () => ctx.reload() }));
    bindStopButtons(tableHost, { onDone: () => ctx.reload() });
  };

  statusSel?.addEventListener('change', () => {
    sessionStorage.setItem('wxread-runs-status', statusSel.value);
    page = 1;
    drawTable();
  });
  triggerSel?.addEventListener('change', () => {
    sessionStorage.setItem('wxread-runs-trigger', triggerSel.value);
    page = 1;
    drawTable();
  });

  drawTable();

  $$('[data-open]', root).forEach((btn) => btn.addEventListener('click', () => {
    runDetailDrawer(Number(btn.dataset.open), { onDone: () => ctx.reload() });
  }));

  ctx.setHandler(bindRunStateRefresh(ctx));
}
