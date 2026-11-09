import { api } from '../api.js';
import { state, refreshAccounts, refreshChannels } from '../state.js';
import { icon } from '../icons.js';
import {
  qrLoginDialog, importAccountDialog, editAccountDialog, startRunDialog, runDetailDrawer, avatarHTML,
} from '../components.js';
import { renderError, skeletonHTML, bindStopButtons } from '../helpers.js';
import {
  $, $$, escapeHtml, relTime, statusBadge, toast, confirmDialog, fmtDuration, fmtMinutes, pageHeaderHTML,
} from '../util.js';

const MODE_LABEL = { custom: '指定书籍', rotate: '随机轮换', sequence: '顺序书单' };

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
    // 先守卫后渲染：路由已切换时旧请求的失败响应不写共享 root。
    if (!ctx.isActive()) return;
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
      <input class="input" id="acc-search" placeholder="搜索序号 / 名称 / 昵称" style="min-width:230px">
      <select class="select" id="acc-filter">
        <option value="all">全部状态</option>
        <option value="active">在线</option>
        <option value="expired">登录失效</option>
        <option value="running">阅读中</option>
      </select>
    </div>

    ${state.accounts.length ? `<div class="table-wrap" id="accounts-grid"></div>
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

  drawCards(ctx);

  let refreshTimer;
  let refreshing = false;
  let refreshAgain = false;
  let disposed = false;
  const refreshCards = async () => {
    if (disposed || !ctx.isActive()) return;
    if (refreshing) {
      refreshAgain = true;
      return;
    }
    refreshing = true;
    try {
      do {
        refreshAgain = false;
        await refreshAccounts();
        if (disposed || !ctx.isActive()) return;
        const sub = $('.page-sub', root);
        if (sub) sub.textContent = `${state.accounts.length} 个账号 · ${state.active.length} 个正在阅读`;
        drawCards(ctx);
      } while (refreshAgain);
    } catch (e) {
      if (!disposed && ctx.isActive()) toast(`刷新账号进度失败：${e.message}`, 'warn');
    } finally {
      refreshing = false;
    }
  };
  const scheduleRefresh = () => {
    if (disposed || !ctx.isActive()) return;
    clearTimeout(refreshTimer);
    refreshTimer = setTimeout(refreshCards, 300);
  };
  ctx.onCleanup(() => {
    disposed = true;
    clearTimeout(refreshTimer);
  });
  ctx.setHandler((ev) => {
    if (ev.type === 'run.start' || ev.type === 'run.tick' || ev.type === 'run.end') scheduleRefresh();
  });
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

// drawCards 重画筛选后的表格（表头 + 居中行）。
function drawCards(ctx) {
  const { root } = ctx;
  const grid = $('#accounts-grid', root);
  if (!grid) return;
  const running = runningMap();
  const list = filteredAccounts(running);
  grid.innerHTML = `
    <table class="table acc-table table-wide">
      <thead><tr>
        <th style="min-width:260px">账号</th>
        <th style="min-width:300px">阅读目标 / 心跳 / 模板</th>
        <th style="width:150px">阅读</th>
        <th style="min-width:170px">问题提醒渠道</th>
        <th style="width:300px">管理</th>
      </tr></thead>
      <tbody>${list.map((a) => accountCard(a, running.get(a.id))).join('')}</tbody>
    </table>
  `;
  const empty = $('#acc-empty', root);
  if (empty) empty.style.display = list.length ? 'none' : '';
  wireCards(ctx, running);
}

function wireCards(ctx, running) {
  const { root } = ctx;

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

  // 问题提醒渠道绑定：下拉即选即存，每个账号可绑定不同渠道。
  $$('[data-push-channel]', root).forEach((sel) => sel.addEventListener('change', async () => {
    const id = Number(sel.dataset.pushChannel);
    const channelId = Number(sel.value) || 0;
    try {
      await api.updateAccount(id, { push_channel_id: channelId });
      toast(channelId ? '已绑定问题提醒渠道' : '已关闭问题提醒', 'ok');
      await refreshAccounts();
      drawCards(ctx);
    } catch (e) {
      toast(e.message, 'error');
      await refreshAccounts();
      drawCards(ctx);
    }
  }));

  $$('[data-action]', root).forEach((item) => {
    item.addEventListener('click', async () => {
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
  const nickname = a.nickname && a.nickname !== a.name ? `${escapeHtml(a.nickname)} · ` : '';
  const goal = Math.max(0, Number(a.daily_goal_minutes) || 0);
  const today = Math.max(0, Number(a.today_seconds) || 0);
  const remaining = Math.max(0, Number(a.daily_remaining_seconds) || 0);
  const dailyStatus = goal
    ? `<span class="badge neutral">每日目标 ${escapeHtml(goal)} 分钟</span>
       <span class="hint">今日已读 ${fmtDuration(today)} · ${remaining ? `还需 ${fmtDuration(remaining)}` : '已达标'}</span>`
    : `<span class="badge neutral">每日目标关闭</span><span class="hint">今日已读 ${fmtDuration(today)}</span>`;
  // 检查信息单独一行，与上一行的 #ID、状态徽标同列同左缘。
  const checkText = a.last_check_at
    ? `上次检查 ${relTime(a.last_check_at, state.tz)}`
    : '尚未校验登录状态';
  return `<tr data-account-id="${a.id}">
    <td>
      <div class="acc-info">
        <div class="avatar">${avatarHTML(a)}</div>
        <div class="acc-info-text">
          <div class="account-name">${escapeHtml(a.name)}</div>
          <div class="acc-idline">
            <span class="badge neutral mono" title="本项目生成的账号 ID">#${escapeHtml(a.seq)}</span>
            ${statusBadge(a.status, !!active)}
          </div>
          ${active ? `
            <div class="acc-progress">
              <div class="row" style="justify-content:space-between;font-size:11px;color:var(--muted);margin-bottom:4px">
                <span>已读 ${fmtMinutes(active.elapsed)} / ${fmtMinutes(active.target)} 分钟</span><span>${pct}%</span>
              </div>
              <div class="bar"><i style="width:${pct}%"></i></div>
            </div>`
          : `<div class="hint acc-checkline">
              ${nickname}${checkText}
              ${a.last_error ? ` · <span style="color:var(--amber)">${escapeHtml(a.last_error)}</span>` : ''}
            </div>`}
        </div>
      </div>
    </td>
    <td>
      <div class="acc-badges">
        <span class="badge neutral">${icon('timer')}默认 ${escapeHtml(a.default_minutes)} 分钟</span>
        ${dailyStatus}
        <span class="badge neutral">${escapeHtml(a.tick_seconds)}s 心跳</span>
        <span class="badge neutral">${escapeHtml(MODE_LABEL[a.mode] || a.mode)}</span>
        ${a.has_payload ? '<span class="badge ok">请求模板已创建</span>' : '<span class="badge neutral">请求模板未创建</span>'}
        ${a.book_title ? `<span class="badge neutral">${icon('book')}${escapeHtml(a.book_title)}</span>` : ''}
      </div>
    </td>
    <td>
      <div class="acc-cta">
        ${active
          ? `<button class="btn" data-start="${a.id}">${icon('play')}进度</button>
             <button class="btn btn-danger" data-stop="${active.run_id}">${icon('stop')}停止</button>`
          : `<button class="btn btn-primary" data-start="${a.id}">${icon('play')}开始阅读</button>`}
      </div>
    </td>
    <td>
      <select class="select" data-push-channel="${a.id}" aria-label="问题提醒渠道"
        style="min-width:150px" title="仅在重试后仍失败或确认登录失效时提醒">
        <option value="0" ${!a.push_channel_id ? 'selected' : ''}>不提醒</option>
        ${state.channels.map((c) => `<option value="${c.id}" ${a.push_channel_id === c.id ? 'selected' : ''}>${escapeHtml(c.name)}</option>`).join('')}
      </select>
    </td>
    <td>
      <div class="acc-ops">
        <button class="btn btn-sm btn-ghost" data-action="check" data-id="${a.id}" title="检查登录状态">${icon('shield')}检查</button>
        <button class="btn btn-sm btn-ghost" data-action="refresh" data-id="${a.id}" title="刷新登录凭据">${icon('refresh')}刷新</button>
        <button class="btn btn-sm btn-ghost" data-action="edit" data-id="${a.id}" title="编辑设置">${icon('edit')}设置</button>
        <button class="btn btn-sm btn-ghost btn-icon" data-action="delete" data-id="${a.id}" title="删除账号" style="color:var(--red)">${icon('trash')}</button>
      </div>
    </td>
  </tr>`;
}
