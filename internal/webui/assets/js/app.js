import { api } from './api.js';
import { state, refreshAccounts, refreshChannels } from './state.js';
import { icon } from './icons.js';
import { redrawCharts } from './charts.js';
import {
  $, $$, escapeHtml, toast, applyTheme, themePreference, resolveTheme, setLoading,
} from './util.js';
import { renderDashboard } from './pages/dashboard.js';
import { renderAccounts } from './pages/accounts.js';
import { renderRuns } from './pages/runs.js';
import { renderSchedules } from './pages/schedules.js';
import { renderPush } from './pages/push.js';
import { renderSettings } from './pages/settings.js';

const PAGES = {
  dashboard: renderDashboard,
  accounts: renderAccounts,
  runs: renderRuns,
  schedules: renderSchedules,
  push: renderPush,
  settings: renderSettings,
};

const NAV = [
  { key: 'dashboard', label: '总览', icon: 'home' },
  { key: 'accounts', label: '账号', icon: 'users' },
  { key: 'runs', label: '记录', icon: 'list' },
  { key: 'schedules', label: '定时', icon: 'calendar' },
  { key: 'push', label: '推送', icon: 'bell' },
  { key: 'settings', label: '设置', icon: 'settings' },
];

let currentHandler = null;
let cleanups = [];
let authActive = false;
let es = null;
let routeToken = 0;

function routeKey() {
  const raw = (location.hash || '').replace(/^#\/?/, '').split('?')[0];
  return PAGES[raw] ? raw : 'dashboard';
}

function setActiveNav(key) {
  $$('[data-nav]').forEach((el) => el.classList.toggle('active', el.dataset.nav === key));
}

function updateNavCount() {
  $$('[data-count="accounts"]').forEach((el) => {
    el.textContent = String(state.accounts.length);
    el.style.display = state.accounts.length ? '' : 'none';
  });
}

function updateConn(on) {
  state.connected = on;
  const el = $('#conn');
  if (!el) return;
  el.className = `conn ${on ? 'online' : 'offline'}`;
  const label = el.querySelector('span:last-child');
  if (label) label.textContent = on ? '实时连接' : '连接断开';
}

async function renderRoute() {
  const token = ++routeToken;
  const key = routeKey();
  state.page = key;
  setActiveNav(key);
  currentHandler = null;
  cleanups.forEach((fn) => { try { fn(); } catch { /* ignore */ } });
  cleanups = [];

  const root = $('#page');
  if (!root) return;
  root.innerHTML = '';

  const ctx = {
    root,
    setHandler: (fn) => { currentHandler = fn; },
    onCleanup: (fn) => { cleanups.push(fn); },
    reload: () => renderRoute(),
  };

  try {
    await PAGES[key](ctx);
    if (token !== routeToken) return;
  } catch (e) {
    root.innerHTML = `<article class="card"><div class="empty">
      <div class="empty-ico">${icon('alert')}</div>
      <h3>页面加载失败</h3><p>${escapeHtml(e.message)}</p>
      <button class="btn" data-retry>重新加载</button>
    </div></article>`;
    root.querySelector('[data-retry]')?.addEventListener('click', () => location.reload());
  }
  window.scrollTo({ top: 0, behavior: 'instant' });
}

function shellHTML(me) {
  const navHTML = NAV.map((n) => `
    <a class="nav-item" href="#/${n.key}" data-nav="${n.key}">
      ${icon(n.icon)}<span>${n.label}</span>
      ${n.key === 'accounts' ? '<span class="nav-count" data-count="accounts"></span>' : ''}
    </a>`).join('');

  const mobileHTML = NAV.map((n) => `
    <a href="#/${n.key}" data-nav="${n.key}">${icon(n.icon)}<span>${n.label}</span></a>`).join('');

  return `<div class="app-shell">
    <aside class="sidebar">
      <div class="brand">
        <div class="brand-mark">${icon('bookOpen')}</div>
        <div class="brand-text">
          <div class="brand-name">${escapeHtml(me.site_name || '读时')}</div>
          <div class="brand-sub">ReadPilot</div>
        </div>
      </div>
      <div class="nav-section">工作台</div>
      ${navHTML}
      <div class="sidebar-footer">
        <div class="conn" id="conn"><span class="dot"></span><span>连接中…</span></div>
        <div class="sidebar-actions">
          <button class="side-btn" id="btn-theme">${icon('sun')}<span>主题</span></button>
          <button class="side-btn" id="btn-logout">${icon('logout')}<span>退出</span></button>
        </div>
      </div>
    </aside>
    <main class="main"><div class="page-inner"><div id="page"></div></div></main>
    <nav class="mobile-nav">${mobileHTML}</nav>
  </div>`;
}

function renderShell(me) {
  authActive = false;
  const app = $('#app');
  app.innerHTML = shellHTML(me);
  document.title = `${me.site_name || '读时'} · ReadPilot`;

  updateNavCount();
  updateConn(false);

  $('#btn-theme').addEventListener('click', () => {
    const cur = document.documentElement.dataset.theme;
    const next = cur === 'dark' ? 'light' : 'dark';
    applyTheme(next);
    redrawCharts();
    $('#btn-theme').innerHTML = `${icon(next === 'dark' ? 'moon' : 'sun')}<span>主题</span>`;
    toast(`已切换为${next === 'dark' ? '深色' : '浅色'}主题`, 'ok');
  });

  $('#btn-logout').addEventListener('click', async () => {
    try { await api.logout(); } catch { /* ignore */ }
    location.reload();
  });
}

function connectEvents() {
  if (es) es.close();
  es = new EventSource('/api/events');
  es.onopen = () => updateConn(true);
  es.addEventListener('hello', () => updateConn(true));
  es.addEventListener('message', (e) => {
    let ev;
    try { ev = JSON.parse(e.data); } catch { return; }
    window.dispatchEvent(new CustomEvent('app:event', { detail: ev }));
    onGlobalEvent(ev);
  });
  es.onerror = () => updateConn(false);
}

function onGlobalEvent(ev) {
  switch (ev.type) {
    case 'run.start':
      toast(`「${ev.data?.account_name || ''}」开始阅读`, 'info');
      refreshAccounts().then(updateNavCount).catch(() => {});
      break;
    case 'run.end': {
      const d = ev.data || {};
      const mins = ((d.elapsed || 0) / 60).toFixed(1);
      if (d.status === 'success') toast(`${d.account_name || ''} 阅读完成，本次 ${mins} 分钟`, 'ok', 4500);
      else if (d.status === 'stopped') toast('阅读已停止', 'warn');
      else toast(`阅读失败：${d.error || '未知错误'}`, 'error', 6000);
      refreshAccounts().then(updateNavCount).catch(() => {});
      break;
    }
    case 'schedule.fire':
      toast(`定时任务已触发：${ev.data?.account_name || ''}`, 'info');
      break;
    case 'schedule.skip':
      toast(`已跳过定时任务：${ev.data?.reason || ''}`, 'warn');
      break;
    case 'schedule.error':
      toast(`定时任务失败：${ev.data?.error || ''}`, 'error');
      break;
    default:
      break;
  }
}

function authHTML(mode) {
  const setup = mode === 'setup';
  return `<div class="auth-minimal">
    <form class="auth-minimal-form" id="auth-form" autocomplete="off">
      <div class="auth-error" id="auth-error">${icon('alert')}<span></span></div>
      ${setup ? `<input class="input auth-input" id="site-name" placeholder="站点名称（可选）" maxlength="40">` : ''}
      <input class="input auth-input" id="password" type="password"
        placeholder="${setup ? '设置管理密码（至少 6 位）' : '管理密码'}"
        autocomplete="${setup ? 'new-password' : 'current-password'}" autofocus>
      ${setup ? `<input class="input auth-input" id="password2" type="password" placeholder="确认密码" autocomplete="new-password">` : ''}
      <button class="btn btn-primary auth-submit" id="auth-submit" type="submit">
        ${setup ? '创建并进入' : '登录'}
      </button>
    </form>
  </div>`;
}

function showAuth(mode, info = {}) {
  authActive = true;
  if (es) { es.close(); es = null; }
  document.title = `${info.site_name || '读时'} · ReadPilot`;
  $('#app').innerHTML = authHTML(mode);

  const form = $('#auth-form');
  const errBox = $('#auth-error');
  const errText = $('#auth-error span');

  const showError = (msg) => {
    errText.textContent = msg;
    errBox.classList.add('show');
  };

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    errBox.classList.remove('show');
    const password = $('#password').value;
    const password2 = $('#password2')?.value;
    if (!password) return showError('请输入密码');
    if (mode === 'setup') {
      if (password.length < 6) return showError('密码至少需要 6 位');
      if (password !== password2) return showError('两次输入的密码不一致');
    }
    const btn = $('#auth-submit');
    setLoading(btn, true);
    try {
      if (mode === 'setup') {
        await api.setup(password, $('#site-name')?.value.trim() || '');
      } else {
        await api.login(password);
      }
      location.reload();
    } catch (err) {
      showError(err.message);
      setLoading(btn, false);
    }
  });
}

async function boot() {
  try {
    const status = await api.status();
    state.status = status;
    if (status.needs_setup) {
      showAuth('setup', status);
      return;
    }
    if (!status.logged_in) {
      showAuth('login', status);
      return;
    }
    const me = await api.me();
    state.tz = me.timezone || state.tz;
    await refreshAccounts();
    await refreshChannels().catch(() => {});
    renderShell(me);
    connectEvents();
    window.addEventListener('hashchange', renderRoute);
    if (!location.hash || !PAGES[location.hash.replace(/^#\/?/, '').split('?')[0]]) {
      location.hash = '#/dashboard';
    }
    await renderRoute();
  } catch (e) {
    $('#app').innerHTML = `<div style="display:grid;place-items:center;min-height:100vh;padding:20px">
      <article class="card" style="max-width:420px;text-align:center">
        <div class="empty-ico" style="margin:0 auto 14px">${icon('alert')}</div>
        <h3 style="margin-bottom:8px">无法连接服务</h3>
        <p class="hint" style="margin-bottom:16px">${escapeHtml(e.message)}</p>
        <button class="btn btn-primary" id="boot-retry">重试</button>
      </article></div>`;
    $('#boot-retry')?.addEventListener('click', () => location.reload());
  }
}

window.addEventListener('auth:expired', () => {
  if (authActive) return;
  toast('登录已过期，请重新登录', 'warn');
  showAuth('login', state.status || {});
});

window.addEventListener('resize', () => {
  clearTimeout(window.__chartResize);
  window.__chartResize = setTimeout(redrawCharts, 180);
});

boot();
