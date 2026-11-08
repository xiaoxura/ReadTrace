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
  return `<tr class="clickable" data-run="${r.id}" tabindex="0" role="button" aria-label="查看运行 #${r.id}">
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
  $$('[data-stop]', root).forEach((btn) => btn.addEventListener('click', async () => {
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
