import { api } from '../api.js';
import { state } from '../state.js';
import { icon } from '../icons.js';
import { barChart, ringSVG, sparkline } from '../charts.js';
import {
  startRunDialog, qrLoginDialog, importAccountDialog, runDetailDrawer, accountSelectHTML,
} from '../components.js';
import {
  renderError, skeletonHTML, runRowHTML, bindRunRows, bindStopButtons, bindCountdown,
} from '../helpers.js';
import {
  $, $$, escapeHtml, fmtMinutes, fmtTime, fmtDuration, humanCron,
  debounce, relativeSeconds, pageHeaderHTML,
} from '../util.js';

const RING_SIZE = 132;
const RING_STROKE = 9;
const RING_C = (2 * Math.PI * (RING_SIZE - RING_STROKE)) / 2;
// 活跃账号超过该数量时，「正在阅读」区从完整大卡切换为紧凑行：
// 每个 hero 大卡约 300px 高，数十个账号会把统计内容顶出几屏之外。
const HERO_LIMIT = 3;

export async function renderDashboard(ctx) {
  const { root } = ctx;
  root.innerHTML = skeletonHTML(['height:64px;width:58%', 'height:170px', 'height:96px', 'height:240px']);

  const saved = Number(sessionStorage.getItem('wxread-dash-account') || 0);
  const accountId = state.accounts.some((a) => a.id === saved) ? saved : 0;

  let data;
  try {
    data = await api.overview(14, accountId);
  } catch (e) {
    // 先守卫后渲染：路由已切换时旧请求的失败响应不写共享 root。
    if (!ctx.isActive()) return;
    renderError(root, e, { onRetry: () => ctx.reload() });
    return;
  }
  if (!ctx.isActive()) return;
  state.tz = data.timezone || state.tz;

  const logs = new Map();
  // 紧凑列表（超过 HERO_LIMIT 个活跃账号）不展示每卡实时日志，
  // 也就不为它们各自拉取 tail 日志，免得首屏 N 个并行请求。
  const activeCount = (data.active || []).length;
  if (activeCount && activeCount <= HERO_LIMIT) {
    await Promise.all((data.active || []).map(async (p) => {
      try {
        const res = await api.runLogs(p.run_id, { tail: true, limit: 5 });
        logs.set(p.run_id, res.logs || []);
      } catch { /* ignore */ }
    }));
  }
  if (!ctx.isActive()) return;

  root.innerHTML = pageHTML(data, logs, accountId);
  bind(ctx, data, accountId);

  const reloadSoon = debounce(() => ctx.reload(), 700);
  ctx.setHandler((ev) => {
    if (ev.type === 'run.tick' && ev.data) {
      applyTick(ev.data);
    } else if (ev.type === 'run.log' && ev.data) {
      appendHeroLog(ev.data);
    } else if (ev.type === 'run.start' || ev.type === 'run.end') {
      reloadSoon();
    }
  });
  bindCountdown(ctx);
}

function pageHTML(data, logs, accountId) {
  const now = new Date();
  let dateText = '';
  try {
    dateText = new Intl.DateTimeFormat('zh-CN', {
      timeZone: state.tz, month: 'long', day: 'numeric', weekday: 'long',
    }).format(now);
  } catch {
    dateText = now.toLocaleDateString();
  }

  const hasAccounts = state.accounts.length > 0;
  const accountsFilter = state.accounts.length > 1
    ? accountSelectHTML(accountId, { includeAll: true, id: 'dash-filter', style: 'width:auto;min-width:130px' })
    : '';

  const activeList = data.active || [];
  const activeHTML = activeList.length > HERO_LIMIT
    ? activeListHTML(activeList)
    : activeList.map((p) => activeHero(p, logs.get(p.run_id) || [])).join('');

  return `
    ${pageHeaderHTML({
      title: '总览',
      sub: `${dateText} · 已连续阅读 ${data.streak_days} 天 · 累计 ${fmtDuration(data.total_seconds)}`,
      actions: `
        ${accountsFilter}
        <button class="btn btn-ghost" id="btn-manage">${icon('users')}<span>账号</span></button>
        <button class="btn btn-primary" id="btn-start">${icon('play')}<span>开始阅读</span></button>`,
    })}

    ${activeHTML}

    ${!hasAccounts ? onboardingHTML() : `
      ${statsHTML(data)}
      <div class="dash-grid">
        ${chartHTML(data)}
        ${sideHTML(data)}
      </div>
      ${recentHTML(data)}
    `}
  `;
}

function statsHTML(data) {
  const delta = data.today_seconds - data.yesterday_seconds;
  let deltaText;
  if (data.yesterday_seconds === 0 && data.today_seconds === 0) deltaText = '今天还没有记录';
  else if (data.yesterday_seconds === 0) deltaText = '昨天没有记录';
  else deltaText = delta >= 0 ? `较昨日多 ${fmtMinutes(delta)} 分钟` : `较昨日少 ${fmtMinutes(-delta)} 分钟`;

  const totalHours = data.total_seconds / 3600;
  const totalValue = data.total_seconds >= 3600 ? totalHours.toFixed(1) : Math.round(data.total_seconds / 60);
  const totalUnit = data.total_seconds >= 3600 ? '小时' : '分钟';

  const weekHours = data.week_seconds / 3600;
  const weekValue = data.week_seconds >= 3600 ? weekHours.toFixed(1) : Math.round(data.week_seconds / 60);
  const weekUnit = data.week_seconds >= 3600 ? '小时' : '分钟';
  const avg = Math.round((data.week_seconds / 60) / 7);

  const spark = sparkline(data.series.slice(-7).map((d) => d.seconds), 92, 26);
  const showDelta = data.yesterday_seconds !== 0 || data.today_seconds !== 0;

  return `<div class="stats">
    <div class="stat">
      <div class="stat-top"><span class="stat-label">今日阅读</span><span class="stat-ico accent">${icon('book')}</span></div>
      <div class="stat-value">${fmtMinutes(data.today_seconds)}<small>分钟</small></div>
      <div class="stat-foot">
        ${showDelta ? `<span class="${delta >= 0 ? 'up' : 'down'}">${delta >= 0 ? '↑' : '↓'}</span>` : ''}${escapeHtml(deltaText)}
        <span style="margin-left:auto">${spark}</span>
      </div>
    </div>
    <div class="stat">
      <div class="stat-top"><span class="stat-label">连续天数</span><span class="stat-ico amber">${icon('spark')}</span></div>
      <div class="stat-value">${escapeHtml(data.streak_days)}<small>天</small></div>
      <div class="stat-foot">${data.today_seconds > 0 ? '今天已完成打卡' : '今天还未阅读'}</div>
    </div>
    <div class="stat">
      <div class="stat-top"><span class="stat-label">累计阅读</span><span class="stat-ico blue">${icon('timer')}</span></div>
      <div class="stat-value">${totalValue}<small>${totalUnit}</small></div>
      <div class="stat-foot">共 ${escapeHtml(data.total_runs)} 次阅读记录</div>
    </div>
    <div class="stat">
      <div class="stat-top"><span class="stat-label">本周阅读</span><span class="stat-ico purple">${icon('calendar')}</span></div>
      <div class="stat-value">${weekValue}<small>${weekUnit}</small></div>
      <div class="stat-foot">日均 ${escapeHtml(avg)} 分钟</div>
    </div>
    <div class="stat">
      <div class="stat-top"><span class="stat-label">账号数量</span><span class="stat-ico accent">${icon('users')}</span></div>
      <div class="stat-value">${escapeHtml(String(data.account_count ?? 0))}<small>个</small></div>
      <div class="stat-foot">${(state.active || []).length} 个正在阅读${data.expired_accounts ? ` · ${data.expired_accounts} 个登录失效` : ' · 全部在线'}</div>
    </div>
    <div class="stat">
      <div class="stat-top"><span class="stat-label">定时任务</span><span class="stat-ico purple">${icon('clock')}</span></div>
      <div class="stat-value">${escapeHtml(String(data.schedule_count ?? 0))}<small>个</small></div>
      <div class="stat-foot">${(data.next_runs || [])[0] ? `下次 ${escapeHtml(humanCron(data.next_runs[0].cron))}` : '还没有启用中的任务'}</div>
    </div>
  </div>`;
}

function chartHTML() {
  return `<article class="card">
    <div class="card-head">
      <div>
        <div class="card-title">${icon('chart')}阅读趋势</div>
        <div class="card-sub">最近 14 天的阅读时长</div>
      </div>
      <div class="chart-legend"><i style="background:var(--accent)"></i>分钟</div>
    </div>
    <div class="chart-host" id="dash-chart"></div>
  </article>`;
}

function sideHTML(data) {
  const next = (data.next_runs || []).map((r) => `
    <div class="list-row">
      <div class="schedule-icon">${icon('clock')}</div>
      <div class="list-main">
        <div class="list-title">${escapeHtml(humanCron(r.cron))}</div>
        <div class="list-sub">${escapeHtml(r.account_name || '')} · ${escapeHtml(r.minutes)} 分钟</div>
      </div>
      <div class="list-time" data-countdown="${escapeHtml(r.next_at)}">${relativeSeconds(r.next_at, state.tz)}</div>
    </div>`).join('');

  return `<article class="card">
    <div class="card-head">
      <div>
        <div class="card-title">${icon('calendar')}接下来</div>
        <div class="card-sub">即将执行的定时任务</div>
      </div>
      <a class="btn btn-sm btn-ghost" href="#/schedules">管理</a>
    </div>
    ${next ? `<div class="list">${next}</div>` : `<div class="empty" style="padding:26px 10px">
      <div class="empty-ico" style="width:42px;height:42px;border-radius:14px">${icon('clock')}</div>
      <p>还没有定时任务。设置一个时间，让阅读自动发生。</p>
      <a class="btn btn-sm" href="#/schedules">新建定时任务</a>
    </div>`}
  </article>`;
}

function recentHTML(data) {
  const rows = (data.recent || []).slice(0, 5).map((r) => runRowHTML(r)).join('');

  return `<article class="card" style="margin-top:16px">
    <div class="card-head">
      <div>
        <div class="card-title">${icon('list')}最近阅读</div>
        <div class="card-sub">点击查看实时日志</div>
      </div>
      <a class="btn btn-sm btn-ghost" href="#/runs">全部记录</a>
    </div>
    ${rows ? `<div class="table-wrap"><table class="table table-compact">
      <thead><tr><th>状态</th><th>账号</th><th>触发</th><th>时长</th><th>进度</th><th>开始时间</th><th></th></tr></thead>
      <tbody>${rows}</tbody>
    </table></div>` : `<div class="empty" style="padding:28px 10px"><p>还没有阅读记录，点击右上角「开始阅读」试试。</p></div>`}
  </article>`;
}

// activeListHTML 活跃账号超过 HERO_LIMIT 时的紧凑列表：一行一个账号，
// 复用 hero 的 data-* 属性口径，applyTick 无需感知两种形态。
function activeListHTML(list) {
  const rows = list.map((p) => {
    const pct = p.target ? Math.min(100, Math.round((p.elapsed / p.target) * 100)) : 0;
    const remaining = Math.max(0, p.target - p.elapsed);
    const name = escapeHtml(p.account_name);
    return `<div class="rm-row" data-run="${p.run_id}" tabindex="0" role="button" aria-label="查看 ${name} 的运行详情">
      <div class="rm-id" aria-hidden="true"><span class="pulse-dot"></span></div>
      <div class="rm-main">
        <div class="rm-name">${name}</div>
        <div class="rm-progress"><div class="bar"><i data-bar style="width:${pct}%"></i></div><b data-pct>${pct}%</b></div>
      </div>
      <div class="rm-numbers">
        <span>已读 <b data-elapsed>${fmtMinutes(p.elapsed)}</b> 分</span>
        <span>剩余 <b data-remaining>${fmtMinutes(remaining)}</b> 分</span>
        <span class="rm-success">成功 <b data-success>${escapeHtml(p.success)}</b> 次</span>
      </div>
      <button class="btn btn-sm btn-ghost rm-stop" data-stop="${p.run_id}" aria-label="停止 ${name}">${icon('stop')}停止</button>
    </div>`;
  }).join('');
  return `<article class="card dash-active">
    <div class="card-head">
      <div>
        <div class="card-title">${icon('play')}正在阅读</div>
        <div class="card-sub">${list.length} 个账号同时运行 · 点行看实时日志，或到「记录」页逐个查看</div>
      </div>
      <a class="btn btn-sm btn-ghost" href="#/runs">全部记录</a>
    </div>
    <div class="rm-list">${rows}</div>
  </article>`;
}

function activeHero(p, logs) {  const pct = p.target ? Math.min(100, (p.elapsed / p.target) * 100) : 0;
  const remaining = Math.max(0, p.target - p.elapsed);
  const logHTML = (logs || []).slice(-4).map((log) => `
    <div class="ln ${escapeHtml(log.level)}"><i>${fmtTime(log.ts, state.tz)}</i>${escapeHtml(log.message)}</div>`).join('');

  return `<section class="hero" data-run="${p.run_id}">
    <div class="hero-top">
      <span class="badge run"><span class="pulse-dot"></span>正在阅读</span>
      <span class="hint">开始于 ${fmtTime(p.started_at, state.tz)} · <a href="#/runs" style="color:var(--accent)">查看完整日志</a></span>
    </div>
    <div class="hero-grid">
      <div class="ring-wrap">
        ${ringSVG(pct / 100, RING_SIZE, RING_STROKE)}
        <div class="ring-center"><b data-pct>${Math.round(pct)}%</b><span>已完成</span></div>
      </div>
      <div class="hero-body">
        <div class="hero-title">${escapeHtml(p.account_name)}<span class="badge neutral">目标 ${fmtMinutes(p.target)} 分钟</span></div>
        <div class="hero-meta">每 ${escapeHtml(p.tick_seconds || 30)} 秒同步一次 · 已成功 <b data-success>${escapeHtml(p.success)}</b> 次 · 失败 ${escapeHtml(p.failed)} 次</div>
        <div class="hero-numbers">
          <div class="hero-num"><b data-elapsed>${fmtMinutes(p.elapsed)}</b><span>已读分钟</span></div>
          <div class="hero-num"><b data-remaining>${fmtMinutes(remaining)}</b><span>剩余分钟</span></div>
          <div class="hero-num"><b data-success2>${escapeHtml(p.success)}</b><span>成功心跳</span></div>
        </div>
        <div class="hero-bar"><i data-bar style="width:${pct}%"></i></div>
      </div>
    </div>
    <div class="hero-log">
      <div class="hero-log-head">
        <span>实时日志</span>
        <button class="btn btn-sm btn-ghost" data-stop="${p.run_id}">${icon('stop')}停止</button>
      </div>
      <div class="mini-log" data-mini-log="${p.run_id}">${logHTML || '<div class="ln info">等待第一次心跳…</div>'}</div>
    </div>
  </section>`;
}

function onboardingHTML() {
  return `<article class="card" style="margin-bottom:16px">
    <div class="empty" style="padding:56px 24px">
      <div class="empty-ico" style="width:64px;height:64px;border-radius:20px">${icon('qr')}</div>
      <h3 style="font-size:17px">先绑定一个微信读书账号</h3>
      <p>扫码登录只需几秒。登录凭据会使用本机密钥加密保存在 SQLite 中，不会上传到任何服务器。</p>
      <div class="row" style="margin-top:8px">
        <button class="btn btn-primary" id="onboard-qr">${icon('qr')}扫码登录</button>
        <button class="btn btn-ghost" id="onboard-import">${icon('link')}粘贴 Cookie</button>
      </div>
      <div class="hint" style="margin-top:18px">绑定后可以设置默认时长、书籍与定时任务。</div>
    </div>
  </article>`;
}

function bind(ctx, data, accountId) {
  const { root } = ctx;

  $('#btn-start', root)?.addEventListener('click', () => {
    startRunDialog({ accountId, onDone: () => ctx.reload() });
  });
  $('#btn-manage', root)?.addEventListener('click', () => { location.hash = '#/accounts'; });
  $('#onboard-qr', root)?.addEventListener('click', () => {
    qrLoginDialog({ onDone: () => ctx.reload() });
  });
  $('#onboard-import', root)?.addEventListener('click', () => {
    importAccountDialog({ onDone: () => ctx.reload() });
  });
  $('#dash-filter', root)?.addEventListener('change', (e) => {
    sessionStorage.setItem('wxread-dash-account', e.target.value);
    ctx.reload();
  });

  const chart = $('#dash-chart', root);
  if (chart) {
    barChart(
      chart,
      (data.series || []).map((d) => ({ label: d.date.slice(5), value: d.seconds })),
      { height: 218, format: (v) => `${fmtMinutes(v)} 分钟` },
    );
  }

  bindRunRows(root, (id) => runDetailDrawer(id, { onDone: () => ctx.reload() }));
  bindStopButtons(root, { onDone: () => ctx.reload() });
}

function applyTick(d) {
  const card = document.querySelector(`.hero[data-run="${d.run_id}"]`);
  if (!card) return;
  const pct = d.target ? Math.min(100, (d.elapsed / d.target) * 100) : 0;
  const ring = card.querySelector('.ring-progress');
  if (ring) ring.style.strokeDashoffset = (RING_C * (1 - pct / 100)).toFixed(1);
  const pctEl = card.querySelector('[data-pct]');
  if (pctEl) pctEl.textContent = `${Math.round(pct)}%`;
  const elapsed = card.querySelector('[data-elapsed]');
  if (elapsed) elapsed.textContent = fmtMinutes(d.elapsed);
  const remaining = card.querySelector('[data-remaining]');
  if (remaining) remaining.textContent = fmtMinutes(Math.max(0, d.target - d.elapsed));
  const success = card.querySelector('[data-success]');
  if (success) success.textContent = d.success;
  const success2 = card.querySelector('[data-success2]');
  if (success2) success2.textContent = d.success;
  const bar = card.querySelector('[data-bar]');
  if (bar) bar.style.width = `${pct}%`;
}

function appendHeroLog(log) {
  const box = document.querySelector(`[data-mini-log="${log.run_id}"]`);
  if (!box) return;
  if (box.querySelector('.ln.info')?.textContent.includes('等待第一次心跳')) box.innerHTML = '';
  const line = document.createElement('div');
  line.className = `ln ${escapeHtml(log.level)}`;
  line.innerHTML = `<i>${fmtTime(log.ts, state.tz)}</i>${escapeHtml(log.message)}`;
  box.appendChild(line);
  while (box.children.length > 4) box.removeChild(box.firstChild);
}
