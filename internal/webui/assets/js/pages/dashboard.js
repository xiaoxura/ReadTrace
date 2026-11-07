import { api } from '../api.js';
import { state } from '../state.js';
import { icon } from '../icons.js';
import { barChart, ringSVG, sparkline } from '../charts.js';
import {
  startRunDialog, qrLoginDialog, importAccountDialog, runDetailDrawer,
} from '../components.js';
import {
  $, $$, escapeHtml, fmtMinutes, fmtDuration, fmtClock, fmtTime, greeting,
  relativeSeconds, humanCron, toast, setLoading,
} from '../util.js';

const RING_SIZE = 132;
const RING_STROKE = 9;
const RING_C = (2 * Math.PI * (RING_SIZE - RING_STROKE)) / 2;

function skeleton() {
  return `<div class="page-loading">
    <div class="skeleton" style="height:64px;width:58%"></div>
    <div class="skeleton" style="height:170px"></div>
    <div class="skeleton" style="height:96px"></div>
    <div class="skeleton" style="height:240px"></div>
  </div>`;
}

export async function renderDashboard(ctx) {
  const { root } = ctx;
  root.innerHTML = skeleton();

  const saved = Number(sessionStorage.getItem('wxread-dash-account') || 0);
  const accountId = state.accounts.some((a) => a.id === saved) ? saved : 0;

  let data;
  try {
    data = await api.overview(14, accountId);
  } catch (e) {
    root.innerHTML = `<div class="card"><div class="empty"><div class="empty-ico">${icon('alert')}</div>
      <h3>加载失败</h3><p>${escapeHtml(e.message)}</p>
      <button class="btn" data-retry>重新加载</button></div></div>`;
    root.querySelector('[data-retry]')?.addEventListener('click', () => location.reload());
    return;
  }
  state.tz = data.timezone || state.tz;

  const logs = new Map();
  await Promise.all((data.active || []).map(async (p) => {
    try {
      const res = await api.runLogs(p.run_id, { tail: true, limit: 5 });
      logs.set(p.run_id, res.logs || []);
    } catch { /* ignore */ }
  }));

  root.innerHTML = pageHTML(data, logs, accountId);
  bind(ctx, data, accountId);

  ctx.setHandler((ev) => {
    if (ev.type === 'run.tick' && ev.data) {
      applyTick(ev.data);
    } else if (ev.type === 'run.log' && ev.data) {
      appendHeroLog(ev.data);
    } else if (ev.type === 'run.start' || ev.type === 'run.end') {
      setTimeout(() => ctx.reload(), 700);
    }
  });

  const timer = setInterval(() => {
    $$('[data-countdown]').forEach((el) => {
      el.textContent = relativeSeconds(Number(el.dataset.countdown), state.tz);
    });
  }, 30000);
  ctx.onCleanup(() => clearInterval(timer));
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
  const todayText = data.today_seconds > 0
    ? `今天已读 ${fmtMinutes(data.today_seconds)} 分钟`
    : '准备读点什么？';
  const greetingText = `${greeting()}，${todayText}`;

  const accountsFilter = state.accounts.length > 1
    ? `<select class="select" id="dash-filter" style="width:auto;min-width:130px">
        <option value="0" ${!accountId ? 'selected' : ''}>全部账号</option>
        ${state.accounts.map((a) => `<option value="${a.id}" ${a.id === accountId ? 'selected' : ''}>${escapeHtml(a.name)}</option>`).join('')}
      </select>`
    : '';

  const activeHTML = (data.active || []).map((p) => activeHero(p, logs.get(p.run_id) || [])).join('');

  return `
    <div class="topbar">
      <div>
        <div class="page-kicker">总览 · ${escapeHtml(dateText)}</div>
        <h1 class="page-title">${escapeHtml(greetingText)}</h1>
        <div class="page-sub">
          ${data.streak_days > 0 ? `已连续阅读 <b style="color:var(--accent)">${data.streak_days}</b> 天` : '开始你的第一次阅读，建立连续记录'}
          · 累计 ${fmtDuration(data.total_seconds)}
        </div>
      </div>
      <div class="top-actions">
        ${accountsFilter}
        <button class="btn btn-ghost" id="btn-manage">${icon('users')}<span>账号</span></button>
        <button class="btn btn-primary" id="btn-start">${icon('play')}<span>开始阅读</span></button>
      </div>
    </div>

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
  else deltaText = `${delta >= 0 ? '较昨日 +' : '较昨日 -'}${fmtMinutes(Math.abs(delta))} 分钟`;

  const totalHours = data.total_seconds / 3600;
  const totalValue = data.total_seconds >= 3600 ? totalHours.toFixed(1) : Math.round(data.total_seconds / 60);
  const totalUnit = data.total_seconds >= 3600 ? '小时' : '分钟';

  const weekHours = data.week_seconds / 3600;
  const weekValue = data.week_seconds >= 3600 ? weekHours.toFixed(1) : Math.round(data.week_seconds / 60);
  const weekUnit = data.week_seconds >= 3600 ? '小时' : '分钟';
  const avg = Math.round((data.week_seconds / 60) / 7);

  const spark = sparkline(data.series.slice(-7).map((d) => d.seconds), 92, 26);

  return `<div class="stats">
    <div class="stat">
      <div class="stat-top"><span class="stat-label">今日阅读</span><span class="stat-ico accent">${icon('book')}</span></div>
      <div class="stat-value">${fmtMinutes(data.today_seconds)}<small>分钟</small></div>
      <div class="stat-foot">
        <span class="${delta >= 0 ? 'up' : 'down'}">${delta >= 0 ? '↑' : '↓'}</span>${escapeHtml(deltaText)}
        <span style="margin-left:auto">${spark}</span>
      </div>
    </div>
    <div class="stat">
      <div class="stat-top"><span class="stat-label">连续天数</span><span class="stat-ico amber">${icon('spark')}</span></div>
      <div class="stat-value">${data.streak_days}<small>天</small></div>
      <div class="stat-foot">${data.today_seconds > 0 ? '今天已完成打卡' : '今天还未阅读'}</div>
    </div>
    <div class="stat">
      <div class="stat-top"><span class="stat-label">累计阅读</span><span class="stat-ico blue">${icon('timer')}</span></div>
      <div class="stat-value">${totalValue}<small>${totalUnit}</small></div>
      <div class="stat-foot">共 ${data.total_runs} 次阅读记录</div>
    </div>
    <div class="stat">
      <div class="stat-top"><span class="stat-label">本周阅读</span><span class="stat-ico purple">${icon('calendar')}</span></div>
      <div class="stat-value">${weekValue}<small>${weekUnit}</small></div>
      <div class="stat-foot">日均 ${avg} 分钟</div>
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
        <div class="list-sub">${escapeHtml(r.account_name || '')} · ${r.minutes} 分钟</div>
      </div>
      <div class="list-time" data-countdown="${r.next_at}">${relativeSeconds(r.next_at, state.tz)}</div>
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
    <div class="section-title">快捷操作</div>
    <div class="row" style="flex-wrap:wrap">
      <button class="btn btn-sm" id="quick-start">${icon('play')}立即阅读</button>
      <a class="btn btn-sm btn-ghost" href="#/accounts">${icon('users')}账号设置</a>
      <a class="btn btn-sm btn-ghost" href="#/push">${icon('bell')}推送</a>
    </div>
  </article>`;
}

function recentHTML(data) {
  const rows = (data.recent || []).map((r) => {
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
      <td class="mono">${fmtMinutes(r.elapsed_seconds)} 分</td>
      <td>${r.trigger === 'schedule' ? '<span class="badge neutral">定时</span>' : '<span class="badge neutral">手动</span>'}</td>
      <td><div class="progress-cell"><div class="bar"><i style="width:${pct}%"></i></div><span>${pct}%</span></div></td>
      <td class="cell-muted">${fmtClock(r.started_at, state.tz)}</td>
      <td>${icon('chevronRight')}</td>
    </tr>`;
  }).join('');

  return `<article class="card" style="margin-top:16px">
    <div class="card-head">
      <div>
        <div class="card-title">${icon('list')}最近阅读</div>
        <div class="card-sub">点击查看实时日志</div>
      </div>
      <a class="btn btn-sm btn-ghost" href="#/runs">全部记录</a>
    </div>
    ${rows ? `<div class="table-wrap"><table class="table table-compact">
      <thead><tr><th>状态</th><th>账号</th><th>时长</th><th>触发</th><th>进度</th><th>开始时间</th><th></th></tr></thead>
      <tbody>${rows}</tbody>
    </table></div>` : `<div class="empty" style="padding:28px 10px"><p>还没有阅读记录，点击右上角「开始阅读」试试。</p></div>`}
  </article>`;
}

function activeHero(p, logs) {
  const pct = p.target ? Math.min(100, (p.elapsed / p.target) * 100) : 0;
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
        <div class="hero-meta">每 ${p.tick_seconds || 30} 秒同步一次 · 已成功 <b data-success>${p.success}</b> 次 · 失败 ${p.failed} 次</div>
        <div class="hero-numbers">
          <div class="hero-num"><b data-elapsed>${fmtMinutes(p.elapsed)}</b><span>已读分钟</span></div>
          <div class="hero-num"><b data-remaining>${fmtMinutes(remaining)}</b><span>剩余分钟</span></div>
          <div class="hero-num"><b data-success2>${p.success}</b><span>成功心跳</span></div>
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
  $('#quick-start', root)?.addEventListener('click', () => {
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

  $$('[data-run]', root).forEach((row) => {
    row.addEventListener('click', () => runDetailDrawer(Number(row.dataset.run), { onDone: () => ctx.reload() }));
  });

  $$('[data-stop]', root).forEach((btn) => {
    btn.addEventListener('click', async () => {
      const id = Number(btn.dataset.stop);
      setLoading(btn, true);
      try {
        await api.stopRun(id);
        toast('已停止本次阅读', 'ok');
        setTimeout(() => ctx.reload(), 500);
      } catch (e) {
        toast(e.message, 'error');
      } finally {
        setLoading(btn, false);
      }
    });
  });
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
  line.className = `ln ${log.level}`;
  line.innerHTML = `<i>${fmtTime(log.ts, state.tz)}</i>${escapeHtml(log.message)}`;
  box.appendChild(line);
  while (box.children.length > 4) box.removeChild(box.firstChild);
}
