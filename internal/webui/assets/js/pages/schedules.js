import { api } from '../api.js';
import { state, refreshAccounts, refreshSchedules } from '../state.js';
import { icon } from '../icons.js';
import { addScheduleDialog } from '../components.js';
import {
  renderError, skeletonHTML, bindCountdown, bindEnabledToggle,
} from '../helpers.js';
import {
  $, $$, escapeHtml, humanCron, fmtClock, relativeSeconds, relTime, confirmDialog, toast,
  pageHeaderHTML,
} from '../util.js';

export async function renderSchedules(ctx) {
  const { root } = ctx;
  root.innerHTML = skeletonHTML(['height:70px;width:45%', 'height:260px']);

  try {
    if (!state.accounts.length) await refreshAccounts();
    if (!ctx.isActive()) return;
    await refreshSchedules();
  } catch (e) {
    renderError(root, e, { onRetry: () => ctx.reload() });
    return;
  }
  if (!ctx.isActive()) return;

  const list = state.schedules;
  const enabled = list.filter((s) => s.enabled).length;

  root.innerHTML = `
    ${pageHeaderHTML({
      kicker: '定时',
      title: '定时任务',
      sub: `${list.length} 个任务 · ${enabled} 个启用中`,
      actions: `<button class="btn btn-primary" id="btn-add">${icon('plus')}<span>新建定时</span></button>`,
    })}

    <article class="card">
      ${list.length ? `<div class="list" style="gap:0">${list.map((s) => scheduleRow(s)).join('')}</div>` : `
        <div class="empty" style="padding:54px 20px">
          <div class="empty-ico" style="width:60px;height:60px;border-radius:18px">${icon('calendar')}</div>
          <h3>还没有定时任务</h3>
          <p>例如设置在每天 01:00 自动阅读 30 分钟，即使忘了也能保持连续天数。</p>
          <button class="btn btn-primary" id="empty-add">${icon('plus')}新建第一个任务</button>
        </div>`}
    </article>

    <div class="note" style="margin-top:16px">
      ${icon('info')}
      <div>定时任务使用标准 5 段 cron 表达式（分 时 日 月 周）。服务需要保持运行，任务才会按时触发。</div>
    </div>
  `;
  if (!ctx.isActive()) return;

  const openAdd = () => addScheduleDialog({ onDone: () => ctx.reload() });
  $('#btn-add', root)?.addEventListener('click', openAdd);
  $('#empty-add', root)?.addEventListener('click', openAdd);

  bindEnabledToggle($$('[data-toggle]', root), {
    call: (input) => api.updateSchedule(Number(input.dataset.toggle), { enabled: input.checked }),
    labels: { on: '已启用，任务将按计划执行', off: '已暂停该任务' },
    onSuccess: async () => {
      await refreshSchedules();
      ctx.reload();
    },
  });

  $$('[data-edit]', root).forEach((btn) => btn.addEventListener('click', () => {
    const s = state.schedules.find((x) => x.id === Number(btn.dataset.edit));
    if (!s) return;
    addScheduleDialog({ schedule: s, onDone: () => ctx.reload() });
  }));

  $$('[data-del]', root).forEach((btn) => btn.addEventListener('click', async () => {
    const s = state.schedules.find((x) => x.id === Number(btn.dataset.del));
    if (!s) return;
    const ok = await confirmDialog({
      title: '删除定时任务',
      message: `确定删除「${humanCron(s.cron)}（${s.account_name || '账号'}）」吗？`,
      confirmText: '删除',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteSchedule(s.id);
      toast('任务已删除', 'ok');
      ctx.reload();
    } catch (e) {
      toast(e.message, 'error');
    }
  }));

  bindCountdown(ctx);
}

function scheduleRow(s) {
  const hasNext = !!(s.enabled && s.next_run_at);
  return `<div class="schedule-row">
    <div class="schedule-icon ${s.enabled ? 'on' : ''}">${icon('clock')}</div>
    <div class="schedule-main">
      <div class="schedule-title">
        ${escapeHtml(humanCron(s.cron))}
        <span class="badge neutral">${escapeHtml(s.account_name || '账号')}</span>
        ${s.enabled ? '<span class="badge ok">已启用</span>' : '<span class="badge neutral">已暂停</span>'}
      </div>
      <div class="schedule-meta">
        <span class="mono">${escapeHtml(s.cron)}</span> · ${s.minutes} 分钟
        · 上次运行 ${s.last_run_at ? relTime(s.last_run_at, state.tz) : '尚未运行'}
      </div>
    </div>
    <div class="schedule-next">
      <b${hasNext ? ` data-countdown="${s.next_run_at}"` : ''}>${hasNext ? relativeSeconds(s.next_run_at, state.tz) : '—'}</b>
      <span>${hasNext ? fmtClock(s.next_run_at, state.tz) : '已暂停'}</span>
    </div>
    <label class="switch" title="${s.enabled ? '暂停' : '启用'}">
      <input type="checkbox" data-toggle="${s.id}" ${s.enabled ? 'checked' : ''} aria-label="${s.enabled ? '暂停任务' : '启用任务'}"><i></i>
    </label>
    <button class="btn btn-sm btn-ghost btn-icon" data-edit="${s.id}" title="编辑" aria-label="编辑任务">${icon('edit')}</button>
    <button class="btn btn-sm btn-ghost btn-icon" data-del="${s.id}" title="删除" aria-label="删除任务">${icon('trash')}</button>
  </div>`;
}
