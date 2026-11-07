import { api } from '../api.js';
import { state, refreshAccounts, refreshChannels } from '../state.js';
import { icon } from '../icons.js';
import {
  qrLoginDialog, importAccountDialog, editAccountDialog, startRunDialog, runDetailDrawer, avatarHTML,
} from '../components.js';
import {
  $, $$, escapeHtml, relTime, statusBadge, toast, setLoading, confirmDialog, fmtMinutes,
} from '../util.js';

const MODE_LABEL = { stable: '稳定模板', custom: '指定书籍', rotate: '随机轮换' };

export async function renderAccounts(ctx) {
  const { root } = ctx;
  root.innerHTML = `<div class="page-loading"><div class="skeleton" style="height:70px;width:50%"></div><div class="skeleton" style="height:200px"></div></div>`;

  try {
    await refreshAccounts();
    if (!state.channels.length) await refreshChannels().catch(() => {});
  } catch (e) {
    root.innerHTML = `<div class="card"><div class="empty"><div class="empty-ico">${icon('alert')}</div><h3>加载失败</h3><p>${escapeHtml(e.message)}</p></div></div>`;
    return;
  }

  const list = state.accounts;
  const running = new Map(state.active.map((p) => [p.account_id, p]));
  const healthy = list.filter((a) => a.status === 'active').length;

  root.innerHTML = `
    <div class="topbar">
      <div>
        <div class="page-kicker">账号</div>
        <h1 class="page-title">微信读书账号</h1>
        <div class="page-sub">共 ${list.length} 个账号${list.length ? ` · ${healthy} 个状态正常 · ${running.size} 个正在阅读` : ''}</div>
      </div>
      <div class="top-actions">
        <button class="btn btn-ghost" id="btn-import">${icon('link')}<span>粘贴 Cookie</span></button>
        <button class="btn btn-primary" id="btn-qr">${icon('qr')}<span>扫码添加</span></button>
      </div>
    </div>

    ${list.length ? `<div class="accounts-grid">${list.map((a) => accountCard(a, running.get(a.id))).join('')}</div>`
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

  const openQR = () => qrLoginDialog({ onDone: () => ctx.reload() });
  const openImport = () => importAccountDialog({ onDone: () => ctx.reload() });

  $('#btn-qr', root)?.addEventListener('click', openQR);
  $('#btn-import', root)?.addEventListener('click', openImport);
  $('#empty-qr', root)?.addEventListener('click', openQR);
  $('#empty-import', root)?.addEventListener('click', openImport);

  const closeMenus = () => $$('.menu.open', root).forEach((m) => m.classList.remove('open'));
  document.addEventListener('click', closeMenus);
  ctx.onCleanup(() => document.removeEventListener('click', closeMenus));

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
      editAccountDialog(acc, { onDone: () => ctx.reload() });
    });
  });

  $$('[data-stop]', root).forEach((btn) => {
    btn.addEventListener('click', async () => {
      setLoading(btn, true);
      try {
        await api.stopRun(Number(btn.dataset.stop));
        toast('已停止', 'ok');
        setTimeout(() => ctx.reload(), 500);
      } catch (e) {
        toast(e.message, 'error');
      } finally {
        setLoading(btn, false);
      }
    });
  });

  $$('[data-action]', root).forEach((item) => {
    item.addEventListener('click', async () => {
      closeMenus();
      const id = Number(item.dataset.id);
      const action = item.dataset.action;
      const acc = state.accounts.find((a) => a.id === id);
      if (action === 'check') {
        toast('正在检查登录状态…', 'info', 1800);
        try {
          const res = await api.checkAccount(id);
          toast(res.ok ? '登录状态正常' : `登录已失效：${res.error || '请重新扫码'}`, res.ok ? 'ok' : 'error');
          await refreshAccounts();
          ctx.reload();
        } catch (e) {
          toast(e.message, 'error');
        }
      } else if (action === 'refresh') {
        toast('正在刷新登录凭据…', 'info', 1800);
        try {
          await api.refreshAccount(id);
          toast('登录凭据已刷新', 'ok');
          await refreshAccounts();
          ctx.reload();
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
          ctx.reload();
        } catch (e) {
          toast(e.message, 'error');
        }
      }
    });
  });
}

function accountCard(a, active) {
  const pct = active && active.target ? Math.min(100, Math.round((active.elapsed / active.target) * 100)) : 0;
  return `<article class="account-card">
    <div class="account-top">
      <div class="avatar">${avatarHTML(a)}</div>
      <div style="flex:1;min-width:0">
        <div class="account-name">${escapeHtml(a.name)} ${statusBadge(a.status, !!active)}</div>
        <div class="account-sub">${escapeHtml(a.nickname || a.vid || '未绑定用户信息')}</div>
      </div>
    </div>

    <div class="account-meta">
      <span class="badge neutral">${icon('timer')}默认 ${a.default_minutes} 分钟</span>
      <span class="badge neutral">${a.tick_seconds}s 心跳</span>
      <span class="badge neutral">${MODE_LABEL[a.mode] || a.mode}</span>
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
