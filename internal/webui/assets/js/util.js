import { icon } from './icons.js';

export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

export function escapeHtml(value) {
  return String(value ?? '').replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

export function fmtDuration(seconds) {
  const s = Math.max(0, Math.round(Number(seconds) || 0));
  if (s < 60) return `${s} 秒`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} 分钟`;
  const h = Math.floor(m / 60);
  const rm = m % 60;
  return rm ? `${h} 小时 ${rm} 分` : `${h} 小时`;
}

export function fmtMinutes(seconds) {
  const s = Math.max(0, Number(seconds) || 0);
  return (s / 60).toFixed(s % 60 === 0 ? 0 : 1);
}

export function fmtClock(ts, tz, withDate = true) {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  const opts = withDate
    ? { timeZone: tz, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }
    : { timeZone: tz, hour: '2-digit', minute: '2-digit', hour12: false };
  try {
    return new Intl.DateTimeFormat('zh-CN', opts).format(d).replace(/\//g, '-');
  } catch {
    return d.toLocaleString();
  }
}

export function fmtTime(ts, tz, withSeconds = false) {
  if (!ts) return '—';
  try {
    const opts = {
      timeZone: tz, hour: '2-digit', minute: '2-digit', hour12: false,
    };
    if (withSeconds) opts.second = '2-digit';
    return new Intl.DateTimeFormat('zh-CN', opts).format(new Date(ts * 1000));
  } catch {
    return withSeconds
      ? new Date(ts * 1000).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
      : new Date(ts * 1000).toLocaleTimeString();
  }
}

export function fmtDay(ts, tz) {
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      timeZone: tz, month: '2-digit', day: '2-digit',
    }).format(new Date(ts * 1000)).replace(/\//g, '-');
  } catch {
    return new Date(ts * 1000).toLocaleDateString();
  }
}

export function relTime(ts, tz) {
  if (!ts) return '—';
  const diff = Date.now() / 1000 - ts;
  if (diff < 45) return '刚刚';
  if (diff < 3600) return `${Math.max(1, Math.round(diff / 60))} 分钟前`;
  if (diff < 86400) return `${Math.round(diff / 3600)} 小时前`;
  if (diff < 86400 * 7) return `${Math.round(diff / 86400)} 天前`;
  return fmtClock(ts, tz);
}

export function initials(name) {
  const s = String(name || '').trim();
  if (!s) return '读';
  return s.slice(0, 1).toUpperCase();
}

export function statusBadge(status, running = false) {
  if (running) return `<span class="badge run"><span class="pulse-dot"></span>阅读中</span>`;
  switch (status) {
    case 'active': return `<span class="badge ok">${icon('check')}正常</span>`;
    case 'expired': return `<span class="badge err">${icon('alert')}已失效</span>`;
    case 'unverified': return `<span class="badge warn">${icon('info')}未验证</span>`;
    default: return `<span class="badge neutral">${escapeHtml(status || '未知')}</span>`;
  }
}

/* ---------- 定时时长与字段级校验（缺陷 06 / 08 共享逻辑） ---------- */

// 读原始输入解析分钟数：仅 1~1440 的整数有效。空串、0、0.5、"3e2"、
// 越界（1441+）、非数字一律返回 null —— 绝不静默回退默认 30（缺陷 08）。
// 新建 / 编辑定时任务共用这一个函数；默认值应体现在输入框的 value / placeholder。
export function parseDurationMinutes(raw) {
  const text = String(raw ?? '').trim();
  if (!/^\d+$/.test(text)) return null;
  return Number.isSafeInteger(Number(text)) && Number(text) >= 1 && Number(text) <= 1440
    ? Number(text)
    : null;
}

// 在字段容器（closest('.field')）就地标注错误：容器加 .invalid（红框）+
// 末尾追加一行 inline 文案；控件重新输入时自动解除该字段错误态。
// 返回字段容器；控件不在 .field 内时返回 null。
export function setFieldError(control, message) {
  const field = control?.closest?.('.field');
  if (!field) return null;
  field.classList.add('invalid');
  let hint = $('.field-error', field);
  if (!hint) {
    hint = document.createElement('div');
    hint.className = 'field-error';
    field.appendChild(hint);
  }
  hint.textContent = message;
  control.addEventListener('input', () => {
    field.classList.remove('invalid');
    hint.remove();
  }, { once: true });
  return field;
}

// 清空 scope 内全部字段错误态（提交前统一重置，避免残留旧提示）。
export function clearFieldErrors(scope = document) {
  $$('.field.invalid', scope).forEach((field) => {
    field.classList.remove('invalid');
    $('.field-error', field)?.remove();
  });
}

// 拒绝提交时定位首个错误字段（缺陷 06）：滚动到视域中心并把焦点交给其
// 中控件；返回是否成功定位。
export function focusFirstError(scope = document) {
  const field = $('.field.invalid', scope);
  const control = field && $('.input, .select, .textarea', field);
  if (!control) return false;
  control.scrollIntoView({ block: 'center', behavior: 'smooth' });
  control.focus({ preventScroll: true });
  return true;
}

/* ---------- cron 解读 ---------- */
export function humanCron(expr) {
  const raw = String(expr || '').trim();
  const f = raw.split(/\s+/);
  if (f.length !== 5) return raw;
  const [mi, ho, dom, mon, dow] = f;
  const isNum = (v) => /^\d+$/.test(v);
  const step = (v) => v.startsWith('*/') ? v.slice(2) : null;

  if (raw === '* * * * *') return '每分钟';
  if (mi.startsWith('*/') && ho === '*' && dom === '*' && mon === '*' && dow === '*') {
    return `每 ${step(mi)} 分钟`;
  }
  if (isNum(mi) && ho.startsWith('*/') && dom === '*' && mon === '*' && dow === '*') {
    return `每 ${step(ho)} 小时（第 ${mi} 分）`;
  }
  const hhmm = (h, m) => `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
  if (isNum(mi) && isNum(ho) && dom === '*' && mon === '*') {
    if (dow === '*') return `每天 ${hhmm(ho, mi)}`;
    const names = { '0': '周日', '7': '周日', '1': '周一', '2': '周二', '3': '周三', '4': '周四', '5': '周五', '6': '周六' };
    if (dow === '1-5') return `工作日 ${hhmm(ho, mi)}`;
    if (dow.includes(',')) {
      const ds = dow.split(',').map((d) => names[d] || d).join('、');
      return `每周 ${ds} ${hhmm(ho, mi)}`;
    }
    if (names[dow]) return `每${names[dow].slice(1)} ${hhmm(ho, mi)}`;
  }
  if (isNum(mi) && isNum(ho) && dom.startsWith('*/') && mon === '*' && dow === '*') {
    return `每 ${step(dom)} 天 ${hhmm(ho, mi)}`;
  }
  return raw;
}

export function toast(message, type = 'info', duration = 3600) {
  let wrap = $('.toasts');
  if (!wrap) {
    wrap = document.createElement('div');
    wrap.className = 'toasts';
    wrap.setAttribute('role', 'status');
    wrap.setAttribute('aria-live', 'polite');
    document.body.appendChild(wrap);
  }
  while (wrap.children.length >= 6) wrap.firstElementChild.remove();
  const icons = { ok: 'check', error: 'alert', warn: 'alert', info: 'info' };
  const el = document.createElement('div');
  el.className = `toast ${type}`;
  el.innerHTML = `${icon(icons[type] || 'info')}<div>${escapeHtml(message)}</div>`;
  wrap.appendChild(el);
  setTimeout(() => {
    el.classList.add('out');
    setTimeout(() => el.remove(), 220);
  }, duration);
}

let dialogDepth = 0;

export function lockScroll() {
  dialogDepth++;
  document.body.style.overflow = 'hidden';
}

export function unlockScroll() {
  dialogDepth = Math.max(0, dialogDepth - 1);
  if (dialogDepth === 0) document.body.style.overflow = '';
}

// 弹窗栈：只有栈顶弹窗响应 Escape，防止一次按键同时关掉确认框与
// 底层编辑弹窗（未保存内容也随之丢）。走 openModal 的都在同一栈里。
const modalStack = [];

export function openModal({ title, subtitle = '', body = '', footer = '', wide, narrow, onClose, onMount }) {
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML = `
    <div class="modal ${wide ? 'wide' : ''} ${narrow ? 'narrow' : ''}" role="dialog" aria-modal="true">
      <div class="modal-head">
        <div>
          <div class="modal-title">${escapeHtml(title)}</div>
          ${subtitle ? `<div class="modal-sub">${escapeHtml(subtitle)}</div>` : ''}
        </div>
        <button class="modal-close" aria-label="关闭">${icon('close')}</button>
      </div>
      <div class="modal-body">${body}</div>
      ${footer ? `<div class="modal-foot">${footer}</div>` : ''}
    </div>`;
  document.body.appendChild(mask);
  lockScroll();

  const previouslyFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const focusables = () => $$(
    'button:not([disabled]), [href], input:not([disabled]):not([type="hidden"]):not([type="checkbox"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    mask,
  ).filter((el) => el.offsetParent !== null);

  const entry = { mask, focusables };
  modalStack.push(entry);

  const close = () => {
    if (!mask.isConnected) return;
    // 先出栈再隐藏，保证 onClose 期间再开新弹窗时栈序正确。
    const idx = modalStack.indexOf(entry);
    if (idx >= 0) modalStack.splice(idx, 1);
    mask.remove();
    unlockScroll();
    document.removeEventListener('keydown', onKey);
    // 焦点恢复（缺陷 05）：优先交还给「打开本弹窗时」的 document.activeElement
    //（confirmDialog 场景即下层弹窗里的触发控件，例如底部「删除」按钮），
    // preventScroll 保住父弹窗滚动位置不再跳回顶；触发控件已断开连接才
    // 回退到下层弹窗首个可聚焦元素，最后才是打开前的元素。
    const below = modalStack[modalStack.length - 1];
    const opener = previouslyFocused?.isConnected ? previouslyFocused : null;
    if (opener) opener.focus({ preventScroll: true });
    else if (below && below.mask.isConnected) below.focusables()[0]?.focus?.();
    else previouslyFocused?.focus?.();
    onClose?.();
  };
  const onKey = (e) => {
    if (e.key === 'Escape') {
      // 只在自己位于栈顶时关自己；下层弹窗不响应这次按键。
      if (modalStack[modalStack.length - 1] !== entry) return;
      close();
      return;
    }
    if (e.key === 'Tab') {
      const list = focusables();
      if (!list.length) return;
      const first = list[0];
      const last = list[list.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  };
  document.addEventListener('keydown', onKey);
  mask.addEventListener('mousedown', (e) => { if (e.target === mask) close(); });
  $('.modal-close', mask).addEventListener('click', close);
  (mask.querySelector('input:not([type="hidden"]), select, textarea') || focusables()[0])?.focus();
  onMount?.(mask, close);
  return { mask, close, modal: $('.modal', mask) };
}

export function confirmDialog({ title, message, confirmText = '确认', danger = false }) {
  return new Promise((resolve) => {
    let settled = false;
    const done = (v) => {
      if (settled) return;
      settled = true;
      resolve(v);
    };
    const { mask, close } = openModal({
      title,
      narrow: true,
      body: `<div class="hint" style="font-size:13px;line-height:1.7;color:var(--text-2)">${escapeHtml(message)}</div>`,
      footer: `<button class="btn btn-ghost" data-cancel>取消</button>
        <button class="btn ${danger ? 'btn-danger' : 'btn-primary'}" data-ok>${escapeHtml(confirmText)}</button>`,
      onClose: () => done(false),
    });
    $('[data-cancel]', mask).addEventListener('click', () => { done(false); close(); });
    $('[data-ok]', mask).addEventListener('click', () => { done(true); close(); });
  });
}

export async function copyText(text) {
  const legacyCopy = () => {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.cssText = 'position:fixed;left:-9999px;top:0;opacity:0';
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
      ok = document.execCommand('copy');
      toast(ok ? '已复制到剪贴板' : '复制失败，请手动选择文本', ok ? 'ok' : 'error');
    } catch {
      toast('复制失败，请手动选择文本', 'error');
    }
    ta.remove();
    return ok;
  };
  try {
    if (!navigator.clipboard) return legacyCopy();
    await navigator.clipboard.writeText(text);
    toast('已复制到剪贴板', 'ok');
    return true;
  } catch {
    return legacyCopy();
  }
}

export function debounce(fn, ms = 300) {
  let t;
  return (...args) => {
    clearTimeout(t);
    t = setTimeout(() => fn(...args), ms);
  };
}

export function setLoading(btn, on) {
  if (!btn) return;
  btn.classList.toggle('loading', on);
  btn.disabled = on;
}

export function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

export function relativeSeconds(ts, tz) {
  if (!ts) return '—';
  const diff = ts - Date.now() / 1000;
  if (diff <= 0) return '即将执行';
  const totalMin = Math.round(diff / 60);
  if (totalMin < 1) return `${Math.round(diff)} 秒后`;
  if (totalMin < 60) return `${totalMin} 分钟后`;
  if (diff < 86400) {
    const h = Math.floor(totalMin / 60);
    const m = totalMin % 60;
    return m ? `${h} 小时 ${m} 分钟后` : `${h} 小时后`;
  }
  return fmtClock(ts, tz);
}

export function downloadText(filename, text) {
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = filename;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

export function themePreference() {
  const v = localStorage.getItem('wxread-theme');
  return v === 'light' || v === 'dark' || v === 'system' ? v : 'light';
}

export function resolveTheme(pref) {
  if (pref === 'system') {
    return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }
  return pref === 'light' ? 'light' : 'dark';
}

const THEME_COLOR = { dark: '#070a0f', light: '#f5f3ee' };

export function syncThemeColor(theme) {
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', THEME_COLOR[theme] || THEME_COLOR.dark);
}

export function applyTheme(pref) {
  localStorage.setItem('wxread-theme', pref);
  const theme = resolveTheme(pref);
  document.documentElement.dataset.theme = theme;
  syncThemeColor(theme);
}
/**
 * 统一页面标题区：单行紧凑布局（标题 + 可选计数小字 + 右侧操作）。
 * title / sub 会被转义，actions 由调用方提供已经转义好的按钮 HTML。
 */
export function pageHeaderHTML({ title = '', sub = '', actions = '' } = {}) {
  return `<div class="topbar">
    <div class="topbar-main">
      <h1 class="page-title">${escapeHtml(title)}</h1>
      ${sub ? `<div class="page-sub">${escapeHtml(sub)}</div>` : ''}
    </div>
    ${actions ? `<div class="top-actions">${actions}</div>` : ''}
  </div>`;
}

/**
 * 页面二级菜单：渲染 tabs + 切换回调。actions 为 tabbar 右侧的操作区
 * HTML（由调用方在重建后自行 wire 事件）。
 */
export function renderTabs({ container, tabs, active, onChange, actions = '' }) {
  container.innerHTML = `<div class="tabs">
    ${tabs.map((t) => `<button class="tab ${t.key === active ? 'active' : ''}" data-tab="${t.key}">${t.label}</button>`).join('')}
    <div class="tabbar-actions">${actions}</div>
  </div>`;
  $$('[data-tab]', container).forEach((btn) => {
    btn.addEventListener('click', () => onChange(btn.dataset.tab));
  });
}
