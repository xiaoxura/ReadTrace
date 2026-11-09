import { api } from './api.js';
import { state, refreshAccounts, refreshChannels } from './state.js';
import { icon } from './icons.js';
import { redrawCharts } from './charts.js';
import {
  $, $$, escapeHtml, toast, applyTheme, setLoading,
} from './util.js';
import { renderDashboard } from './pages/dashboard.js';
import { renderAccounts } from './pages/accounts.js';
import { renderRuns } from './pages/runs.js';
import { renderPool } from './pages/pool.js';
import { renderSchedules } from './pages/schedules.js';
import { renderPush } from './pages/push.js';
import { renderSettings } from './pages/settings.js';
import { renderAbout } from './pages/about.js';
import { renderChangelog } from './pages/changelog.js';

const PAGES = {
  dashboard: renderDashboard,
  accounts: renderAccounts,
  runs: renderRuns,
  pool: renderPool,
  schedules: renderSchedules,
  push: renderPush,
  settings: renderSettings,
  about: renderAbout,
  changelog: renderChangelog,
};

const NAV = [
  { key: 'dashboard', label: '总览', icon: 'home' },
  { key: 'accounts', label: '账号', icon: 'users' },
  { key: 'runs', label: '记录', icon: 'list' },
  { key: 'pool', label: '书池', icon: 'book' },
  { key: 'schedules', label: '定时', icon: 'calendar' },
  { key: 'push', label: '推送', icon: 'bell' },
  { key: 'settings', label: '设置', icon: 'settings' },
  { key: 'about', label: '关于', icon: 'info' },
  { key: 'changelog', label: '更新日志', icon: 'megaphone' },
];

// 移动端入口更多了，「更多」弹层已移除：底部导航横向滚动展示全部页面。
const PAGE_TITLES = {
  dashboard: '总览', accounts: '账号', runs: '记录', pool: '书池', schedules: '定时',
  push: '推送', settings: '设置', about: '关于', changelog: '更新日志',
};

let currentHandler = null;
let cleanups = [];
let authActive = false;
let es = null;
let esReconnect = null;
let esDelay = 1000;
let everConnected = false;
let routeToken = 0;

function hashKey(raw = location.hash) {
  return (raw || '').replace(/^#\/?/, '').split('?')[0];
}

function routeKey() {
  const key = hashKey();
  return PAGES[key] ? key : 'dashboard';
}

function setActiveNav(key) {
  $$('[data-nav]').forEach((el) => {
    const active = el.dataset.nav === key;
    el.classList.toggle('active', active);
    if (active) el.setAttribute('aria-current', 'page');
    else el.removeAttribute('aria-current');
  });
  // 底部导航条目已超出一屏，切换页面时把当前项滚进可视区域。
  const mobileNav = $('.mobile-nav');
  const activeItem = mobileNav?.querySelector(`[data-nav="${key}"]`);
  if (mobileNav && activeItem && mobileNav.clientWidth > 0) {
    mobileNav.scrollTo({
      left: Math.max(0, activeItem.offsetLeft - (mobileNav.clientWidth - activeItem.offsetWidth) / 2),
      behavior: 'smooth',
    });
  }
}

function updateNavCount() {
  $$('[data-count="accounts"]').forEach((el) => {
    el.textContent = String(state.accounts.length);
    el.style.display = state.accounts.length ? '' : 'none';
  });
}

function updateConn(on) {
  const prev = state.connected;
  state.connected = on;
  if (prev === true && on === false) toast('实时连接已断开，正在重连…', 'warn');
  if (prev === false && on === true && everConnected) toast('实时连接已恢复', 'ok', 2500);
  if (on) everConnected = true;
  const el = $('#conn');
  if (!el) return;
  el.className = `conn ${on ? 'online' : 'offline'}`;
  el.querySelector('[data-conn-label]')?.replaceChildren(on ? '实时连接' : '正在重连…');
}

async function renderRoute() {
  const token = ++routeToken;
  const key = routeKey();
  state.page = key;
  document.title = `${PAGE_TITLES[key] || '总览'} · ${state.status?.site_name || '阅迹'}`;
  setActiveNav(key);
  currentHandler = null;
  cleanups.forEach((fn) => { try { fn(); } catch { /* ignore */ } });
  cleanups = [];

  const root = $('#page');
  if (!root) return;
  root.innerHTML = '';

  const ctx = {
    root,
    isActive: () => token === routeToken,
    setHandler: (fn) => { if (token === routeToken) currentHandler = fn; },
    onCleanup: (fn) => { if (token === routeToken) cleanups.push(fn); },
    reload: () => { if (token === routeToken) renderRoute(); },
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
    root.querySelector('[data-retry]')?.addEventListener('click', () => renderRoute());
  }
  window.scrollTo({ top: 0, behavior: 'instant' });
}

function shellHTML(me) {
  const navHTML = NAV.map((n) => `
    <a class="nav-item" href="#/${n.key}" data-nav="${n.key}">
      ${icon(n.icon)}<span>${n.label}</span>
      ${n.key === 'accounts' ? '<span class="nav-count" data-count="accounts"></span>' : ''}
    </a>`).join('');

  const mobileHTML = NAV.map((n) =>
    `<a href="#/${n.key}" data-nav="${n.key}">${icon(n.icon)}<span>${n.label}</span></a>`).join('');

  return `<div class="app-shell">
    <aside class="sidebar">
      <div class="brand">
        <div class="brand-mark">${icon('bookOpen')}</div>
        <div class="brand-text">
          <div class="brand-name">${escapeHtml(me.site_name || '阅迹')}</div>
          <div class="brand-sub">ReadTrace</div>
        </div>
      </div>
      <div class="nav-section">工作台</div>
      ${navHTML}
      <div class="sidebar-footer">
        <div class="conn" id="conn"><span class="dot"></span><span data-conn-label>连接中…</span></div>
        <div class="sidebar-actions">
          <button class="side-btn" id="btn-theme">${icon('sun')}<span>主题</span></button>
          <button class="side-btn" id="btn-logout">${icon('logout')}<span>退出</span></button>
        </div>
      </div>
    </aside>
    <main class="main"><div class="page-inner"><div id="page"></div></div></main>
    <nav class="mobile-nav">
      ${mobileHTML}
    </nav>
  </div>`;
}

function renderShell(me) {
  authActive = false;
  const app = $('#app');
  app.innerHTML = shellHTML(me);
  document.title = `${me.site_name || '阅迹'} · ReadTrace`;

  updateNavCount();
  state.connected = false;

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

function teardownEvents() {
  clearTimeout(esReconnect);
  if (es) {
    es.close();
    es = null;
  }
}

function connectEvents() {
  clearTimeout(esReconnect);
  if (es) es.close();
  es = new EventSource('/api/events');
  es.onopen = () => {
    esDelay = 1000;
    updateConn(true);
  };
  es.addEventListener('hello', () => updateConn(true));
  es.addEventListener('message', (e) => {
    let ev;
    try { ev = JSON.parse(e.data); } catch { return; }
    window.dispatchEvent(new CustomEvent('app:event', { detail: ev }));
    onGlobalEvent(ev);
    currentHandler?.(ev);
  });
  es.onerror = () => {
    updateConn(false);
    if (es?.readyState === EventSource.CLOSED) scheduleReconnect();
  };
}

function scheduleReconnect() {
  if (es) {
    es.close();
    es = null;
  }
  clearTimeout(esReconnect);
  esReconnect = setTimeout(async () => {
    if (authActive) return;
    try {
      await api.me();
      connectEvents();
    } catch {
      scheduleReconnect();
    }
  }, esDelay);
  esDelay = Math.min(30000, esDelay * 2);
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
      ${setup ? `<input class="input auth-input" id="site-name" placeholder="站点名称（可选）" maxlength="40" aria-label="站点名称">` : ''}
      <input class="input auth-input" id="password" type="password"
        placeholder="${setup ? '设置管理密码（至少 6 位）' : '管理密码'}"
        aria-label="${setup ? '设置管理密码' : '管理密码'}"
        autocomplete="${setup ? 'new-password' : 'current-password'}" autofocus>
      ${setup ? `<input class="input auth-input" id="password2" type="password" placeholder="确认密码" aria-label="确认密码" autocomplete="new-password">` : ''}
      <button class="btn btn-primary auth-submit" id="auth-submit" type="submit">
        ${setup ? '创建并进入' : '登录'}
      </button>
    </form>
  </div>`;
}

function showAuth(mode, info = {}) {
  authActive = true;
  teardownEvents();
  currentHandler = null;
  cleanups = [];
  document.title = `${info.site_name || '阅迹'} · ReadTrace`;
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
    if (!location.hash || !PAGES[hashKey()]) {
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
