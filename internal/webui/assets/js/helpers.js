import { api } from './api.js';
import { state } from './state.js';
import { icon } from './icons.js';
import {
  $, $$, escapeHtml, toast, setLoading, debounce, relativeSeconds, fmtMinutes, fmtClock,
} from './util.js';

/* ---------- 页面脚手架（骨架屏 / 错误态） ---------- */

export function skeletonHTML(blocks = ['height:90px']) {
  return `<div class="page-loading">${blocks
    .map((b) => `<div class="skeleton" style="${b}"></div>`)
    .join('')}</div>`;
}

/**
 * 仪表盘专属骨架屏，贴合最终渲染结构（Header + 6个统计指标 + 图表/定时栅格 + 最近记录表格），
 * 消除初次加载与账号切换时的布局抖动 (CLS)。
 */
export function dashboardSkeletonHTML() {
  const statItem = `
    <div class="stat" style="min-height:108px">
      <div class="stat-top">
        <div class="skeleton" style="height:12px;width:56px"></div>
        <div class="skeleton" style="width:28px;height:28px;border-radius:9px"></div>
      </div>
      <div class="skeleton" style="height:30px;width:96px;margin:4px 0"></div>
      <div class="skeleton" style="height:11px;width:70%"></div>
    </div>`;

  return `
    <div class="topbar">
      <div class="topbar-main">
        <div class="skeleton" style="height:26px;width:80px"></div>
        <div class="skeleton" style="height:14px;width:240px"></div>
      </div>
      <div class="top-actions">
        <div class="skeleton" style="height:36px;width:72px;border-radius:10px"></div>
        <div class="skeleton" style="height:36px;width:98px;border-radius:10px"></div>
      </div>
    </div>

    <div class="stats">
      ${statItem.repeat(6)}
    </div>

    <div class="dash-grid">
      <div class="card" style="min-height:290px">
        <div class="card-head">
          <div>
            <div class="skeleton" style="height:16px;width:100px;margin-bottom:6px"></div>
            <div class="skeleton" style="height:12px;width:140px"></div>
          </div>
          <div class="skeleton" style="height:12px;width:40px"></div>
        </div>
        <div class="skeleton" style="height:200px;border-radius:10px;margin-top:10px"></div>
      </div>

      <div class="card" style="min-height:290px">
        <div class="card-head">
          <div>
            <div class="skeleton" style="height:16px;width:80px;margin-bottom:6px"></div>
            <div class="skeleton" style="height:12px;width:120px"></div>
          </div>
        </div>
        <div class="list" style="gap:14px;margin-top:10px">
          ${`<div class="skeleton-row" style="padding:8px 0">
            <div class="skeleton" style="width:36px;height:36px;border-radius:11px;flex:none"></div>
            <div style="flex:1;display:flex;flex-direction:column;gap:6px">
              <div class="skeleton" style="height:14px;width:45%"></div>
              <div class="skeleton" style="height:11px;width:65%"></div>
            </div>
            <div class="skeleton" style="height:14px;width:50px;flex:none"></div>
          </div>`.repeat(3)}
        </div>
      </div>
    </div>

    <div class="card" style="margin-top:16px">
      <div class="card-head">
        <div class="skeleton" style="height:16px;width:90px"></div>
        <div class="skeleton" style="height:28px;width:70px;border-radius:8px"></div>
      </div>
      <div style="display:flex;flex-direction:column;gap:12px;margin-top:10px">
        ${`<div class="skeleton" style="height:38px;border-radius:8px"></div>`.repeat(4)}
      </div>
    </div>
  `;
}

/**
 * 结构化表格骨架屏，用于记录、定时任务、推送渠道等列表页面。
 */
export function tableSkeletonHTML({ titleWidth = 90, rows = 6 } = {}) {
  return `
    <div class="topbar">
      <div class="topbar-main">
        <div class="skeleton" style="height:26px;width:${titleWidth}px"></div>
        <div class="skeleton" style="height:14px;width:160px"></div>
      </div>
      <div class="top-actions">
        <div class="skeleton" style="height:36px;width:88px;border-radius:10px"></div>
      </div>
    </div>
    <div class="card">
      <div class="skeleton" style="height:20px;width:100%;margin-bottom:14px"></div>
      <div style="display:flex;flex-direction:column;gap:10px">
        ${`<div class="skeleton" style="height:44px;border-radius:8px"></div>`.repeat(rows)}
      </div>
    </div>
  `;
}

/**
 * 结构化卡片列表骨架屏，用于账号管理、设置等页面。
 */
export function cardListSkeletonHTML({ titleWidth = 80, count = 3 } = {}) {
  return `
    <div class="topbar">
      <div class="topbar-main">
        <div class="skeleton" style="height:26px;width:${titleWidth}px"></div>
        <div class="skeleton" style="height:14px;width:180px"></div>
      </div>
      <div class="top-actions">
        <div class="skeleton" style="height:36px;width:96px;border-radius:10px"></div>
      </div>
    </div>
    <div class="accounts-grid" style="grid-template-columns:repeat(auto-fill, minmax(300px, 1fr))">
      ${`<div class="card" style="min-height:160px;display:flex;flex-direction:column;gap:14px">
        <div class="skeleton-row">
          <div class="skeleton" style="width:46px;height:46px;border-radius:14px;flex:none"></div>
          <div style="flex:1;display:flex;flex-direction:column;gap:6px">
            <div class="skeleton" style="height:15px;width:40%"></div>
            <div class="skeleton" style="height:12px;width:60%"></div>
          </div>
        </div>
        <div class="skeleton" style="height:12px;width:80%"></div>
        <div class="skeleton-row" style="margin-top:auto;gap:8px">
          <div class="skeleton" style="height:32px;flex:1;border-radius:8px"></div>
          <div class="skeleton" style="height:32px;flex:1;border-radius:8px"></div>
        </div>
      </div>`.repeat(count)}
    </div>
  `;
}

export function renderError(root, err, { onRetry } = {}) {
  if (!root) return;
  root.innerHTML = `<div class="card"><div class="empty">
    <div class="empty-ico">${icon('alert')}</div>
    <h3>加载失败</h3><p>${escapeHtml(err?.message || String(err || '未知错误'))}</p>
    ${onRetry ? '<button class="btn" data-retry>重新加载</button>' : ''}
  </div></div>`;
  root.querySelector('[data-retry]')?.addEventListener('click', onRetry);
}

/* ---------- 共享小组件 ---------- */

export function runStatusBadge(status, runningLabel = '进行中') {
  switch (status) {
    case 'running': return `<span class="badge run"><span class="pulse-dot"></span>${runningLabel}</span>`;
    case 'success': return '<span class="badge ok">成功</span>';
    case 'stopped': return '<span class="badge neutral">已停止</span>';
    default: return '<span class="badge err">失败</span>';
  }
}

export function runRowHTML(r, { endTime = false } = {}) {
  const pct = r.plan_seconds ? Math.min(100, Math.round((r.elapsed_seconds / r.plan_seconds) * 100)) : 0;
  // 失败原因只呈现在运行详情的日志里（引擎结束前会先落一条 error 日志），
  // 列表里只保留状态徽标。
  return `<tr class="clickable" data-run="${r.id}" data-run-group="${r.id}" data-status="${escapeHtml(r.status)}" data-trigger="${escapeHtml(r.trigger)}" tabindex="0" role="button" aria-label="查看运行 #${r.id}">
    <td>${runStatusBadge(r.status)}</td>
    <td class="cell-strong">${escapeHtml(r.account_name || '')}</td>
    <td>${r.trigger === 'schedule' ? '<span class="badge neutral">定时</span>' : '<span class="badge neutral">手动</span>'}</td>
    <td class="mono">${fmtMinutes(r.elapsed_seconds)} 分</td>
    <td><div class="progress-cell"><div class="bar"><i style="width:${pct}%"></i></div><span>${pct}%</span></div></td>
    <td class="cell-muted">${fmtClock(r.started_at, state.tz)}</td>
    ${endTime ? `<td class="cell-muted">${r.finished_at ? fmtClock(r.finished_at, state.tz) : '—'}</td>` : ''}
    <td>${icon('chevronRight')}</td>
  </tr>`;
}

/* ---------- 共享事件绑定 ---------- */

export function bindRunRows(root, openRun) {
  $$('[data-run]', root).forEach((row) => {
    const id = Number(row.dataset.run);
    const open = () => openRun(id);
    row.addEventListener('click', open);
    row.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        open();
      }
    });
  });
}

export function bindStopButtons(root, { onDone } = {}) {
  $$('[data-stop]', root).forEach((btn) => btn.addEventListener('click', async (e) => {
    // 停止按钮可能嵌在可点击行（总览紧凑运行行/账号列表）里，
    // 阻止冒泡避免触发行本身的「打开运行详情」。
    e.stopPropagation();
    setLoading(btn, true);
    try {
      await api.stopRun(Number(btn.dataset.stop));
      toast('已停止', 'ok');
      setTimeout(() => onDone?.(), 500);
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      setLoading(btn, false);
    }
  }));
}

export function bindEnabledToggle(entries, { call, labels = {}, onSuccess } = {}) {
  const { on = '已启用', off = '已停用' } = labels;
  entries.forEach((input) => input.addEventListener('change', async () => {
    try {
      await call(input);
      toast(input.checked ? on : off, 'ok');
      await onSuccess?.(input);
    } catch (e) {
      input.checked = !input.checked;
      toast(e.message, 'error');
    }
  }));
}

export function bindCountdown(ctx, ms = 30000) {
  const timer = setInterval(() => {
    $$('[data-countdown]').forEach((el) => {
      el.textContent = relativeSeconds(Number(el.dataset.countdown), state.tz);
    });
  }, ms);
  ctx.onCleanup(() => clearInterval(timer));
}

export function bindRunStateRefresh(ctx) {
  const reload = debounce(() => ctx.reload(), 700);
  return (ev) => {
    if (ev.type === 'run.start' || ev.type === 'run.end') reload();
  };
}

/* ---------- 客户端分页 ---------- */

export const PAGE_SIZES = [10, 20, 50, 100];

export function pageSlice(items, page, pageSize) {
  const start = (Math.max(1, page) - 1) * pageSize;
  return items.slice(start, start + pageSize);
}

// pagerHTML 底部工具条：总数 + 每页条数选择 + 上/下页。翻页事件用 bindPager 绑定。
export function pagerHTML(total, page, pageSize) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(Math.max(1, page), pages);
  return `
    <div class="table-pager" data-pager>
      <span class="hint">共 ${total} 条</span>
      <span class="table-pager-gap"></span>
      <span class="hint">第 ${safePage} / ${pages} 页</span>
      <select class="select pager-size" style="width:auto;min-width:110px" aria-label="每页条数">
        ${PAGE_SIZES.map((sz) => `<option value="${sz}" ${sz === pageSize ? 'selected' : ''}>${sz} 条 / 页</option>`).join('')}
      </select>
      <button class="btn btn-sm btn-ghost" data-pager-prev ${safePage <= 1 ? 'disabled' : ''}>上一页</button>
      <button class="btn btn-sm btn-ghost" data-pager-next ${safePage >= pages ? 'disabled' : ''}>下一页</button>
    </div>
  `;
}

export function bindPager(host, { onPage, onSize } = {}) {
  const pager = $('[data-pager]', host);
  if (!pager) return;
  $('[data-pager-prev]', pager)?.addEventListener('click', () => onPage?.(-1));
  $('[data-pager-next]', pager)?.addEventListener('click', () => onPage?.(1));
  $('.pager-size', pager)?.addEventListener('change', (e) => onSize?.(Number(e.target.value)));
}
