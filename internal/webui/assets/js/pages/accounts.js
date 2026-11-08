import { api } from '../api.js';
import { state, refreshAccounts, refreshChannels } from '../state.js';
import { icon } from '../icons.js';
import {
  qrLoginDialog, importAccountDialog, editAccountDialog, startRunDialog, runDetailDrawer, avatarHTML,
} from '../components.js';
import { renderError, skeletonHTML, bindStopButtons } from '../helpers.js';
import {
  $, $$, escapeHtml, relTime, statusBadge, toast, confirmDialog, fmtMinutes, pageHeaderHTML,
} from '../util.js';

const MODE_LABEL = { stable: '稳定模板', custom: '指定书籍', rotate: '随机轮换' };

// 筛选状态：模块级，整页切换离开后天然失效。
let accFilter = 'all';
let accQuery = '';

export async function renderAccounts(ctx) {
  const { root } = ctx;
  root.innerHTML = skeletonHTML(['height:44px;width:60%', 'height:200px']);

  try {
    await refreshAccounts();
    if (!ctx.isActive()) return;
    if (!state.channels.length) await refreshChannels().catch(() => {});
  } catch (e) {
    renderError(root, e, { onRetry: () => ctx.reload() });
    return;
  }
  if (!ctx.isActive()) return;

  accFilter = 'all';
  accQuery = '';

  root.innerHTML = `
    ${pageHeaderHTML({
      title: '账号管理',
      sub: `${state.accounts.length} 个账号 · ${state.active.length} 个正在阅读`,
      actions: `
        <button class="btn btn-ghost" id="btn-import">${icon('link')}<span>粘贴 Cookie</span></button>
        <button class="btn btn-primary" id="btn-qr">${icon('qr')}<span>扫码添加</span></button>`,
    })}

    <div class="tool-row">
      <input class="input" id="acc-search" placeholder="搜索序号 / 名称 / 昵称 / 书籍" style="min-width:230px">
      <select class="select" id="acc-filter">
        <option value="all">全部状态</option>
        <option value="active">在线</option>
        <option value="expired">登录失效</option>
        <option value="running">阅读中</option>
      </select>
    </div>

    ${state.accounts.length ? `<div class="accounts-grid" id="accounts-grid"></div>
      <div class="hint" id="acc-empty" style="display:none;margin-top:10px">没有符合筛选条件的账号。</div>`
      : `<article class="card"><div class="empty" style="padding:64px 24px">
          <div class="empty-ico" style="width:64px;height:64px;border-radius:20px">${icon('qr')}</div>
          <h3 style="font-size:17px">还没有绑定账号</h3>
          <p>使用微信扫码登录，或从浏览器粘贴一次阅读请求。登录凭据只会加密保存在本机。</p>
          <div class="row" style="margin-top:8px">
            <button class="btn btn-primary" id="empty-qr">${icon('qr')}扫码登录</button>
            <button class="btn btn-ghost" id="empty-import">${icon('link')}导入 Cookie</button>
          </div>
        </div></article>`}
  `;
  if (!ctx.isActive()) return;

  const openQR = () => qrLoginDialog({ onDone: () => ctx.reload() });
  const openImport = () => importAccountDialog({ onDone: () => ctx.reload() });
  $('#btn-qr', root)?.addEventListener('click', openQR);
  $('#btn-import', root)?.addEventListener('click', openImport);
  $('#empty-qr', root)?.addEventListener('click', openQR);
  $('#empty-import', root)?.addEventListener('click', openImport);

  $('#acc-search', root)?.addEventListener('input', (e) => {
    accQuery = e.target.value.trim().toLowerCase();
    drawCards(ctx);
  });
  $('#acc-filter', root)?.addEventListener('change', (e) => {
    accFilter = e.target.value;
    drawCards(ctx);
  });

  document.addEventListener('click', closeMenus);
  ctx.onCleanup(() => document.removeEventListener('click', closeMenus));

  drawCards(ctx);
}

function runningMap() {
  const map = new Map(state.active.map((p) => [p.account_id, p]));
  return map;
}

function filteredAccounts(running) {
  const q = accQuery;
  return state.accounts.filter((a) => {
    if (accFilter === 'active' && a.status !== 'active') return false;
    if (accFilter === 'expired' && (a.status === 'active' || running.has(a.id))) return false;
    // 阅读中的账号视为在在线之上；失效的不算 running。
    if (accFilter === 'running' && !running.has(a.id)) return false;
    if (!q) return true;
    const hay = [a.id, a.seq, a.name, a.nickname, a.vid, a.book_title, a.book_id]
      .map((x) => String(x ?? '').toLowerCase()).join(' ');
    return hay.includes(q);
  });
}

// drawCards charts the filtered grid and (re)wires per-card events.
function drawCards(ctx) {
  const { root } = ctx;
  const grid = $('#accounts-grid', root);
  if (!grid) return;
  const running = runningMap();
  const list = filteredAccounts(running);
  grid.innerHTML = list.map((a) => accountCard(a, running.get(a.id))).join('');
  const empty = $('#acc-empty', root);
  if (empty) empty.style.display = list.length ? 'none' : '';
  wireCards(ctx, running);
}

function closeMenus() {
  $$('.menu.open').forEach((m) => m.classList.remove('open'));
}

function wireCards(ctx, running) {
  const { root } = ctx;

  $$('[data-menu-btn]', root).forEach((btn) => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const menu = btn.parentElement.querySelector('.menu');
      const isOpen = menu.classList.contains('open');
      closeMenus();
      if (!isOpen) menu.classList.add('open');
    });
  });

  $$('[data-start]', root).forEach((btn) => {
    btn.addEventListener('click', () => {
      const acc = state.accounts.find((a) => a.id === Number(btn.dataset.start));
      if (!acc) return;
      const p = running.get(acc.id);
      if (p) {
        runDetailDrawer(p.run_id, { onDone: () => ctx.reload() });
        return;
      }
      startRunDialog({ accountId: acc.id, onDone: () => ctx.reload() });
    });
  });

  $$('[data-edit]', root).forEach((btn) => {
    btn.addEventListener('click', () => {
      const acc = state.accounts.find((a) => a.id === Number(btn.dataset.edit));
      if (!acc) return;
      editAccountDialog(acc, { onDone: () => ctx.reload() });
    });
  });

  bindStopButtons(root, { onDone: () => ctx.reload() });

  $$('[data-action]', root).forEach((item) => {
    item.addEventListener('click', async () => {
      closeMenus();
      const id = Number(item.dataset.id);
      const action = item.dataset.action;
      const acc = state.accounts.find((a) => a.id === id);
      if (!acc) return;
      if (action === 'check') {
        toast('正在检查登录状态…', 'info', 1800);
        try {
          const res = await api.checkAccount(id);
          if (res.ok) {
            toast('登录状态正常', 'ok');
          } else if (res.status === 'error') {
            toast(`检查失败：${res.error || '可能是网络或接口波动，请稍后重试'}`, 'warn', 5000);
          } else {
            toast(`登录已失效：${res.error || '请重新扫码'}`, 'error', 6000);
          }
          await refreshAccounts();
          drawCards(ctx);
        } catch (e) {
          toast(e.message, 'error');
        }
      } else if (action === 'refresh') {
        toast('正在刷新登录凭据…', 'info', 1800);
        try {
          await api.refreshAccount(id);
          toast('登录凭据已刷新', 'ok');
          await refreshAccounts();
          drawCards(ctx);
        } catch (e) {
          toast(e.message, 'error');
        }
      } else if (action === 'edit') {
        editAccountDialog(acc, { onDone: () => ctx.reload() });
      } else if (action === 'delete') {
        const ok = await confirmDialog({
          title: '删除账号',
          message: `确定删除「${acc.name}」吗？该账号的运行记录、日志与定时任务会一并删除。`,
          confirmText: '删除',
          danger: true,
        });
        if (!ok) return;
        try {
          await api.deleteAccount(id);
          await refreshAccounts();
          toast('账号已删除', 'ok');
          drawCards(ctx);
        } catch (e) {
          toast(e.message, 'error');
        }
      }
    });
  });
}

function accountCard(a, active) {
  const pct = active && active.target ? Math.min(100, Math.round((active.elapsed / active.target) * 100)) : 0;
  return `<article class="account-card" data-account-id="${a.id}">
    <div class="account-top">
      <div class="avatar">${avatarHTML(a)}</div>
      <div style="flex:1;min-width:0">
        <div class="account-name">${escapeHtml(a.name)} <span class="badge neutral mono" title="本项目生成的账号 ID">#${escapeHtml(a.seq)}</span> ${statusBadge(a.status, !!active)}</div>
        <div class="account-sub">${escapeHtml(a.nickname && a.nickname !== a.name ? a.nickname : (a.vid ? '微信读书账号' : '未绑定用户信息'))}</div>
      </div>
    </div>

    <div class="account-meta">
      <span class="badge neutral">${icon('timer')}默认 ${escapeHtml(a.default_minutes)} 分钟</span>
      <span class="badge neutral">${escapeHtml(a.tick_seconds)}s 心跳</span>
      <span class="badge neutral">${escapeHtml(MODE_LABEL[a.mode] || a.mode)}</span>
      ${a.has_payload ? '<span class="badge ok">自定义模板</span>' : ''}
      ${a.book_title ? `<span class="badge neutral">${icon('book')}${escapeHtml(a.book_title)}</span>` : ''}
    </div>

    ${active ? `<div>
      <div class="row" style="justify-content:space-between;font-size:11px;color:var(--muted);margin-bottom:6px">
        <span>本次已读 ${fmtMinutes(active.elapsed)} / ${fmtMinutes(active.target)} 分钟</span><span>${pct}%</span>
      </div>
      <div class="bar"><i style="width:${pct}%"></i></div>
    </div>` : ''}

    <div class="hint">
      ${a.last_check_at ? `上次检查 ${relTime(a.last_check_at, state.tz)}` : '尚未校验登录状态'}
      ${a.last_error ? ` · <span style="color:var(--amber)">${escapeHtml(a.last_error)}</span>` : ''}
    </div>

    <div class="account-actions">
      ${active
        ? `<button class="btn btn-sm" data-start="${a.id}">${icon('play')}查看进度</button>
           <button class="btn btn-sm btn-danger" data-stop="${active.run_id}">${icon('stop')}停止</button>`
        : `<button class="btn btn-sm btn-primary" data-start="${a.id}">${icon('play')}开始阅读</button>
           <button class="btn btn-sm btn-ghost" data-edit="${a.id}">${icon('edit')}设置</button>`}
      <div class="menu-wrap">
        <button class="btn btn-sm btn-ghost btn-icon" data-menu-btn aria-label="更多">${icon('more')}</button>
        <div class="menu">
          <button class="menu-item" data-action="check" data-id="${a.id}">${icon('shield')}检查登录状态</button>
          <button class="menu-item" data-action="refresh" data-id="${a.id}">${icon('refresh')}刷新登录凭据</button>
          <button class="menu-item" data-action="edit" data-id="${a.id}">${icon('settings')}编辑设置</button>
          <div class="menu-sep"></div>
          <button class="menu-item danger" data-action="delete" data-id="${a.id}">${icon('trash')}删除账号</button>
        </div>
      </div>
    </div>
  </article>`;
}
