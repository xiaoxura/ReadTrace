import { api } from '../api.js';
import { state, refreshAccounts } from '../state.js';
import { icon } from '../icons.js';
import { runDetailDrawer, startRunDialog } from '../components.js';
import {
  $, $$, escapeHtml, fmtMinutes, fmtClock, toast, setLoading,
} from '../util.js';

export async function renderRuns(ctx) {
  const { root } = ctx;
  root.innerHTML = `<div class="page-loading"><div class="skeleton" style="height:70px;width:45%"></div><div class="skeleton" style="height:300px"></div></div>`;

  if (!state.accounts.length) {
    try { await refreshAccounts(); } catch { /* ignore */ }
  }
  const saved = Number(sessionStorage.getItem('wxread-runs-account') || 0);
  const accountId = state.accounts.some((a) => a.id === saved) ? saved : 0;

  let data;
  try {
    data = await api.runs(accountId, 120);
  } catch (e) {
    root.innerHTML = `<div class="card"><div class="empty"><div class="empty-ico">${icon('alert')}</div><h3>加载失败</h3><p>${escapeHtml(e.message)}</p></div></div>`;
    return;
  }

  const active = data.active || [];
  const rows = data.runs || [];

  root.innerHTML = `
    <div class="topbar">
      <div>
        <div class="page-kicker">运行记录</div>
        <h1 class="page-title">每次阅读都有据可查</h1>
        <div class="page-sub">共 ${rows.length} 条记录${active.length ? ` · ${active.length} 个正在运行` : ''}</div>
      </div>
      <div class="top-actions">
        <select class="select" id="run-filter" style="width:auto;min-width:120px">
          <option value="0" ${!accountId ? 'selected' : ''}>全部账号</option>
          ${state.accounts.map((a) => `<option value="${a.id}" ${a.id === accountId ? 'selected' : ''}>${escapeHtml(a.name)}</option>`).join('')}
        </select>
        <button class="btn btn-ghost" id="btn-refresh">${icon('refresh')}<span>刷新</span></button>
        <button class="btn btn-primary" id="btn-start">${icon('play')}<span>开始阅读</span></button>
      </div>
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
              <div class="list-sub">已读 ${fmtMinutes(p.elapsed)} / ${fmtMinutes(p.target)} 分钟 · 成功 ${p.success} 次</div>
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
      ${rows.length ? `<div class="table-wrap"><table class="table table-wide">
        <thead><tr><th>状态</th><th>账号</th><th>触发</th><th>时长</th><th>进度</th><th>开始时间</th><th>结束时间</th><th></th></tr></thead>
        <tbody>${rows.map((r) => {
          const pct = r.plan_seconds ? Math.min(100, Math.round((r.elapsed_seconds / r.plan_seconds) * 100)) : 0;
          const badge = r.status === 'running'
            ? '<span class="badge run"><span class="pulse-dot"></span>进行中</span>'
            : r.status === 'success'
              ? '<span class="badge ok">成功</span>'
              : r.status === 'stopped'
                ? '<span class="badge neutral">已停止</span>'
                : '<span class="badge err">失败</span>';
          return `<tr class="clickable" data-run="${r.id}">
            <td>${badge}</td>
            <td class="cell-strong">${escapeHtml(r.account_name || '')}</td>
            <td>${r.trigger === 'schedule' ? '<span class="badge neutral">定时</span>' : '<span class="badge neutral">手动</span>'}</td>
            <td class="mono">${fmtMinutes(r.elapsed_seconds)} 分</td>
            <td><div class="progress-cell"><div class="bar"><i style="width:${pct}%"></i></div><span>${pct}%</span></div></td>
            <td class="cell-muted">${fmtClock(r.started_at, state.tz)}</td>
            <td class="cell-muted">${r.finished_at ? fmtClock(r.finished_at, state.tz) : '—'}</td>
            <td>${icon('chevronRight')}</td>
          </tr>`;
        }).join('')}</tbody>
      </table></div>` : `<div class="empty" style="padding:44px 10px">
        <div class="empty-ico">${icon('list')}</div><h3>还没有阅读记录</h3>
        <p>点击右上角「开始阅读」，或创建一个定时任务。</p>
      </div>`}
    </article>
  `;

  $('#run-filter', root)?.addEventListener('change', (e) => {
    sessionStorage.setItem('wxread-runs-account', e.target.value);
    ctx.reload();
  });
  $('#btn-refresh', root)?.addEventListener('click', () => ctx.reload());
  $('#btn-start', root)?.addEventListener('click', () => {
    startRunDialog({ accountId, onDone: () => ctx.reload() });
  });
  $$('[data-run]', root).forEach((row) => row.addEventListener('click', () => {
    runDetailDrawer(Number(row.dataset.run), { onDone: () => ctx.reload() });
  }));
  $$('[data-open]', root).forEach((btn) => btn.addEventListener('click', () => {
    runDetailDrawer(Number(btn.dataset.open), { onDone: () => ctx.reload() });
  }));
  $$('[data-stop]', root).forEach((btn) => btn.addEventListener('click', async () => {
    setLoading(btn, true);
    try {
      await api.stopRun(Number(btn.dataset.stop));
      toast('已停止', 'ok');
      setTimeout(() => ctx.reload(), 500);
    } catch (e) {
      toast(e.message, 'error');
      setLoading(btn, false);
    }
  }));

  ctx.setHandler((ev) => {
    if (ev.type === 'run.end' || ev.type === 'run.start') {
      setTimeout(() => ctx.reload(), 600);
    }
  });
}
