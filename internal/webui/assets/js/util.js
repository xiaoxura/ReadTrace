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

export function fmtNumber(seconds) {
  const s = Math.max(0, Math.round(Number(seconds) || 0));
  if (s < 60) return String(s);
  return String(Math.round(s / 6) / 10);
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

export function fmtTime(ts, tz) {
  if (!ts) return '—';
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      timeZone: tz, hour: '2-digit', minute: '2-digit', hour12: false,
    }).format(new Date(ts * 1000));
  } catch {
    return new Date(ts * 1000).toLocaleTimeString();
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

export function greeting() {
  const h = new Date().getHours();
  if (h < 6) return '夜深了';
  if (h < 11) return '早上好';
  if (h < 14) return '中午好';
  if (h < 18) return '下午好';
  return '晚上好';
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
    document.body.appendChild(wrap);
  }
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

let modalStack = 0;

export function openModal({ title, subtitle = '', body = '', footer = '', wide, narrow, onClose, onMount }) {
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML = `
    <div class="modal ${wide ? 'wide' : ''} ${narrow ? 'narrow' : ''}" role="dialog" aria-modal="true">
      <div class="modal-head">
        <div>
          <div class="modal-title">${escapeHtml(title)}</div>
          ${subtitle ? `<div class="modal-sub">${subtitle}</div>` : ''}
        </div>
        <button class="modal-close" aria-label="关闭">${icon('close')}</button>
      </div>
      <div class="modal-body">${body}</div>
      ${footer ? `<div class="modal-foot">${footer}</div>` : ''}
    </div>`;
  document.body.appendChild(mask);
  modalStack++;
  document.body.style.overflow = 'hidden';

  const close = () => {
    if (!mask.isConnected) return;
    mask.remove();
    modalStack = Math.max(0, modalStack - 1);
    if (modalStack === 0) document.body.style.overflow = '';
    document.removeEventListener('keydown', onKey);
    onClose?.();
  };
  const onKey = (e) => { if (e.key === 'Escape') close(); };
  document.addEventListener('keydown', onKey);
  mask.addEventListener('mousedown', (e) => { if (e.target === mask) close(); });
  $('.modal-close', mask).addEventListener('click', close);
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
  try {
    await navigator.clipboard.writeText(text);
    toast('已复制到剪贴板', 'ok');
    return true;
  } catch {
    toast('复制失败，请手动选择文本', 'error');
    return false;
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
  return v === 'light' || v === 'dark' || v === 'system' ? v : 'dark';
}

export function resolveTheme(pref) {
  if (pref === 'system') {
    return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }
  return pref === 'light' ? 'light' : 'dark';
}

export function applyTheme(pref) {
  localStorage.setItem('wxread-theme', pref);
  document.documentElement.dataset.theme = resolveTheme(pref);
}
