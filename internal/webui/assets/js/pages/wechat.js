import { api } from '../api.js';
import { state } from '../state.js';
import { icon } from '../icons.js';
import { renderError, skeletonHTML } from '../helpers.js';
import { openModal } from '../util.js';
import {
  $, $$, escapeHtml, fmtTime, relTime, toast, setLoading, renderTabs, pageHeaderHTML,
  confirmDialog,
} from '../util.js';

const TABS = [
  { key: 'bots', label: '机器人' },
  { key: 'owners', label: '用户与账号' },
  { key: 'chat', label: '对话记录' },
];

let statusData = null;
let activeTab = 'bots';
let ownersQuery = '';
let chatUser = '';
let chatOldest = 0;
let chatSeq = 0;
let chatLive = null; // { user, logBox, moreHolder, seen }

export async function renderWechat(ctx) {
  const { root } = ctx;
  root.innerHTML = skeletonHTML(['height:64px;width:50%', 'height:300px', 'height:220px']);

  activeTab = 'bots';
  ownersQuery = '';
  chatUser = '';
  chatOldest = 0;
  resetChatLive();
  try {
    statusData = await api.wxBotStatus();
  } catch (e) {
    renderError(root, e, { onRetry: () => ctx.reload() });
    return;
  }
  if (!ctx.isActive()) return;

  root.innerHTML = `
    ${pageHeaderHTML({
      kicker: '微信',
      title: '微信机器人',
      sub: '在微信里登录账号、开始/停止阅读、查统计与定时任务',
      actions: `<button class="btn btn-ghost" id="wx-refresh">${icon('refresh')}<span>刷新</span></button>`,
    })}
    <div class="tabs-host"></div>
    <div class="wechat-body"></div>
  `;

  $('#wx-refresh', root)?.addEventListener('click', () => ctx.reload());
  drawTabs(ctx, root);
  drawTab(ctx, root);

  // 对话记录的实时增量；只在聊天 tab 打开时生效。
  ctx.setHandler((ev) => {
    if (ev.type === 'bot.message') onBotMessage(ev.data);
  });
}

function drawTabs(ctx, root) {
  renderTabs({
    container: $('.tabs-host', root),
    tabs: TABS,
    active: activeTab,
    onChange: (key) => {
      if (key === activeTab) return;
      activeTab = key;
      if (key !== 'chat') resetChatLive();
      drawTabs(ctx, root);
      drawTab(ctx, root);
    },
  });
}

function drawTab(ctx, root) {
  if (activeTab !== 'chat') resetChatLive();
  const host = $('.wechat-body', root);
  if (!host) return;
  switch (activeTab) {
    case 'owners': renderOwnersTab(ctx, host); break;
    case 'chat': renderChatTab(ctx, host); break;
    default: renderBotsTab(ctx, host); break;
  }
}

function rootOf(el) { return el.closest('#page'); }

// ---------- 机器人 ----------

function botState(s) {
  if (!s.bound) return { label: '未绑定', tone: 'neutral' };
  if (s.needs_rebind) return { label: '凭据失效', tone: 'err' };
  if (!s.enabled) return { label: '已停用', tone: 'warn' };
  return { label: '运行中', tone: 'ok' };
}

function healthHTML(s) {
  if (!s.bound) return '';
  if (s.needs_rebind) {
    return `<div class="note danger bot-health">${icon('alert')}<div>机器人凭据已被服务端拒绝（-14），长轮询已停止。请点上方「重新扫码绑定」。</div></div>`;
  }
  if (s.last_error) {
    return `<div class="note warn bot-health">${icon('alert')}<div>最近一次长轮询失败：${escapeHtml(s.last_error)}</div></div>`;
  }
  if (s.last_poll_at) {
    return `<div class="hint bot-health">最近心跳 ${relTime(s.last_poll_at, state.tz)}</div>`;
  }
  return `<div class="hint bot-health">等待首次长轮询…</div>`;
}

function renderBotsTab(ctx, host) {
  host.innerHTML = `
    <article class="card bot-hero">
      ${botHeroHTML(statusData || {})}
    </article>
    <div class="dash-grid" style="align-items:start">
      <article class="card">
        <div class="card-head"><div><div class="card-title">${icon('list')}微信内指令</div><div class="card-sub">用户在聊天里发给机器人</div></div></div>
        <div class="kv"><span>菜单 / 帮助</span><b>查看全部指令</b></div>
        <div class="kv"><span>登录</span><b>扫码绑定微信读书账号</b></div>
        <div class="kv"><span>我的账号</span><b>已绑定账号列表</b></div>
        <div class="kv"><span>开始 序号 分钟</span><b>立即开始阅读</b></div>
        <div class="kv"><span>停止 序号</span><b>停止阅读（缺省为自己绑定的全部）</b></div>
        <div class="kv"><span>进度 / 今日 / 最近</span><b>进度与统计</b></div>
        <div class="kv"><span>定时 列表 / 新增 / 开 / 关 / 删</span><b>定时任务管理</b></div>
        <div class="kv"><span>解绑 序号</span><b>解除账号绑定</b></div>
      </article>
      <article class="card">
        <div class="card-head"><div><div class="card-title">${icon('shield')}权限模型</div></div></div>
        <div class="note" style="margin-bottom:10px">${icon('check')}<div>谁扫微信读书的登录码，谁就是该账号在微信侧的唯一所有者；一个微信用户可绑多个账号，一个账号只属于一人（重新扫码即换绑）。</div></div>
        <div class="note" style="margin-bottom:10px">${icon('users')}<div>微信用户只能查看/操作自己绑定的账号及其定时任务、统计与记录；管理员可见全部。</div></div>
        <div class="note">${icon('alert')}<div>绑定面板二维码的扫码人自动成为管理员。凭据均加密落库；对话记录随「数据保留天数」清理。</div></div>
      </article>
    </div>
  `;
  wireBots(ctx, host);
}

function botHeroHTML(s) {
  const bound = !!s.bound;
  const enabled = !!s.enabled;
  const stateInfo = botState(s);
  const lastPoll = s.last_poll_at ? relTime(s.last_poll_at, state.tz) : '—';
  return `
    <div class="card-head">
      <div>
        <div class="card-title">${icon('wechat')}机器人</div>
        <div class="card-sub">微信官方 iLink Bot 渠道</div>
      </div>
      <span class="badge ${stateInfo.tone}">${stateInfo.label}</span>
    </div>
    <div class="bot-facts">
      <div class="bot-fact"><span>机器人 ID</span><b>${bound ? escapeHtml(String(s.bot_id || '—')) : '未绑定'}</b></div>
      <div class="bot-fact"><span>微信用户</span><b>${s.users ?? 0} 人</b></div>
      <div class="bot-fact"><span>绑定账号</span><b>${s.bindings ?? 0} 个</b></div>
      <div class="bot-fact"><span>运行结束直推</span><b>${bound ? (s.push_runs ? '开启' : '关闭') : '—'}</b></div>
      <div class="bot-fact"><span>最近心跳</span><b>${bound ? lastPoll : '—'}</b></div>
    </div>
    <div class="bot-card-actions">
      <button class="btn btn-sm btn-primary" data-wx-bind>${icon('qr')}${bound ? '重新扫码绑定' : '绑定机器人'}</button>
      ${bound ? `<button class="btn btn-sm" data-wx-toggle>${enabled ? icon('stop') + '停用渠道' : icon('play') + '启用渠道'}</button>` : ''}
      ${bound ? `<button class="btn btn-sm" data-wx-push>${icon('bell')}${s.push_runs ? '关闭直推' : '开启直推'}</button>` : ''}
      ${bound ? `<button class="btn btn-sm btn-danger" data-wx-logout>${icon('logout')}断开</button>` : ''}
    </div>
    ${healthHTML(s)}
    ${bound ? '' : '<div class="hint" style="margin-top:10px">点击「绑定机器人」，用手机微信扫码并确认；扫码人自动成为管理员。</div>'}
  `;
}

function wireBots(ctx, host) {
  const s = statusData || {};
  const root = rootOf(host);

  $('[data-wx-toggle]', host)?.addEventListener('click', async (e) => {
    setLoading(e.currentTarget, true);
    try {
      await api.wxBotEnabled(!s.enabled);
      toast(s.enabled ? '渠道已停用' : '渠道已启用', 'ok');
      await refreshData(ctx, root);
    } catch (e2) { toast(e2.message, 'error'); }
    finally { setLoading(e.currentTarget, false); }
  });

  $('[data-wx-push]', host)?.addEventListener('click', async (e) => {
    setLoading(e.currentTarget, true);
    try {
      await api.wxBotPush(!s.push_runs);
      toast(s.push_runs ? '已关闭运行时直推' : '运行结束将直推微信', 'ok');
      await refreshData(ctx, root);
    } catch (e2) { toast(e2.message, 'error'); }
    finally { setLoading(e.currentTarget, false); }
  });

  $('[data-wx-bind]', host)?.addEventListener('click', () => {
    openBotBindDialog(ctx, root, () => refreshData(ctx, root));
  });

  $('[data-wx-logout]', host)?.addEventListener('click', async (e) => {
    const ok = await confirmDialog({
      title: '断开机器人',
      message: '断开后机器人停止收发消息，但用户与绑定关系会保留；下次扫码可重新连接。',
      confirmText: '断开',
      danger: true,
    });
    if (!ok) return;
    setLoading(e.currentTarget, true);
    try {
      await api.wxBotLogout();
      toast('已断开', 'ok');
      await refreshData(ctx, root);
    } catch (e2) { toast(e2.message, 'error'); }
    finally { setLoading(e.currentTarget, false); }
  });
}

async function refreshData(ctx, root) {
  try {
    statusData = await api.wxBotStatus();
  } catch (e) {
    toast(e.message, 'error');
  }
  drawTabs(ctx, root);
  drawTab(ctx, root);
}

// 绑定弹窗（含二维码轮询 + OTP）。
function openBotBindDialog(ctx, root, onDone) {
  let timer = 0;
  let drawn = false;
  let lastQR = '';
  const { mask, close } = openModal({
    title: '绑定微信机器人',
    subtitle: '用手机微信扫码并确认；手机显示数字时在下方输入提交。扫码人自动成为管理员。',
    body: `<div class="wx-qr-zone"><div class="hint">正在申请二维码…</div></div>`,
    onClose: () => clearInterval(timer),
  });
  const zone = $('.wx-qr-zone', mask);

  const draw = (st) => {
    if (st.status === 'failed') {
      zone.innerHTML = `<div class="note warn">${icon('alert')}<div>${escapeHtml(st.error || '绑定失败，请重试')}</div></div>`;
      return true;
    }
    if (st.status === 'confirmed') {
      zone.innerHTML = `<div class="note">${icon('check')}<div>✅ 绑定成功${st.bot_id ? `：机器人 ${escapeHtml(String(st.bot_id))}` : ''}。扫码微信（${escapeHtml(String(st.user_id || '管理员'))}）已是管理员。</div></div>`;
      setTimeout(() => { close(); onDone?.(); }, 1400);
      return true;
    }
    zone.innerHTML = `
      ${st.qr ? `<div style="background:#fff;display:inline-block;padding:10px;border-radius:12px"><img width="240" height="240" alt="绑定二维码" src="${st.qr}"></div>` : ''}
      <div class="hint" data-otp-hint style="margin-top:8px">${st.status === 'need_otp'
        ? escapeHtml(st.error || '📱 手机上显示了数字，请在下方输入并提交：')
        : '📱 请用手机微信扫码并确认；手机若显示 4 位数字，在下方输入提交。'}</div>
      <div class="grid-2" style="margin-top:8px;max-width:420px">
        <input class="input" data-otp placeholder="手机显示的数字验证码" maxlength="8">
        <button class="btn btn-primary" data-otp-btn>${icon('check')}提交验证码</button>
      </div>
    `;
    lastQR = st.qr || lastQR;
    $('[data-otp-btn]', zone)?.addEventListener('click', async (e) => {
      const btn = e.currentTarget;
      const code = ($('[data-otp]', zone).value || '').trim();
      if (!code) { toast('请先输入手机上显示的数字', 'warn'); return; }
      setLoading(btn, true);
      try {
        const out = await api.wxBotLoginOTP(code);
        draw(out);
      } catch (e2) { toast(e2.message, 'error'); }
      finally { setLoading(btn, false); }
    });
    return false;
  };

  (async () => {
    let first;
    try {
      first = await api.wxBotLogin();
    } catch (e) {
      zone.innerHTML = `<div class="note warn">${icon('alert')}<div>${escapeHtml(e.message)}</div></div>`;
      return;
    }
    if (draw(first)) return;
    timer = setInterval(async () => {
      if (!mask.isConnected) { clearInterval(timer); return; }
      if (drawn) return;
      try {
        const st = await api.wxBotLoginStatus();
        if (!st) return;
        if (st.qr && st.qr !== lastQR) {
          lastQR = st.qr;
          const img = $('.wx-qr-zone img', zone);
          if (img) img.src = st.qr;
        }
        if (st.status === 'confirmed' || st.status === 'failed') {
          drawn = true;
          clearInterval(timer);
          draw(st);
          return;
        }
        if (st.status === 'need_otp') {
          const hint = $('[data-otp-hint]', zone);
          if (hint) hint.textContent = st.error ? `⚠️ ${st.error}` : '📱 手机上显示了数字，请在下方输入并提交：';
        }
      } catch { /* transient: keep polling */ }
    }, 2000);
  })();
}

// ---------- 用户与账号 ----------

async function renderOwnersTab(ctx, host) {
  host.innerHTML = `<article class="card">
    <div class="card-head"><div><div class="card-title">${icon('users')}用户与账号</div><div class="card-sub">一个账号同一时间只属于一个微信用户；管理员可操作全部</div></div></div>
    <div data-owner-body><div class="hint">加载中…</div></div>
  </article>`;
  const body = host.querySelector('[data-owner-body]');

  let data;
  try {
    data = await api.wxBotUsers();
  } catch (e) {
    body.innerHTML = `<div class="note warn">${icon('alert')}<div>${escapeHtml(e.message)}</div></div>`;
    return;
  }
  if (!ctx.isActive()) return;

  const users = data.users || [];
  const accounts = data.accounts || [];
  const binds = data.bindings || [];
  if (!users.length) {
    body.innerHTML = `<div class="empty" style="padding:44px 16px">
      <div class="empty-ico" style="width:52px;height:52px;border-radius:16px">${icon('users')}</div>
      <h3>还没有微信用户</h3>
      <p>扫码绑定机器人后，让用户给机器人发送「菜单」，该用户就会出现在这里。</p>
    </div>`;
    return;
  }

  const byOwner = {};
  binds.forEach((b) => { (byOwner[b.user_id] ||= []).push(b); });
  const boundIds = new Set(binds.map((b) => b.account_id));
  const freeAccounts = accounts.filter((a) => !boundIds.has(a.id));
  const root = rootOf(host);

  body.innerHTML = `
    <div class="tool-row">
      <input class="input" data-owner-search placeholder="搜索微信用户 / 账号名 / 序号" style="min-width:220px" value="${escapeHtml(ownersQuery)}">
      <span class="hint" style="margin-left:auto">${users.length} 位用户 · ${binds.length} 条绑定</span>
    </div>
    <div data-owner-rows>${ownersRowsHTML(users, byOwner, accounts, ownersQuery)}</div>
    <div class="field" style="margin-top:18px" data-bind-form>
      <label class="label">手动绑定面板账号</label>
      <div class="row" style="flex-wrap:wrap;align-items:stretch">
        <select class="select" data-bind-uid style="flex:1;min-width:180px">
          ${users.map((u) => `<option value="${escapeHtml(u.user_id)}">${escapeHtml(u.user_id)}${u.is_admin ? '（管理员）' : ''}</option>`).join('')}
        </select>
        <select class="select" data-bind-acc style="flex:1;min-width:180px" ${freeAccounts.length ? '' : 'disabled'}>
          ${freeAccounts.map((a) => `<option value="${a.id}">【${escapeHtml(String(a.seq ?? '#'))}】${escapeHtml(a.name || a.nickname || '账号')}</option>`).join('')
            || '<option value="">（没有未绑定的面板账号）</option>'}
        </select>
        <button class="btn btn-primary" data-bind-btn ${freeAccounts.length ? '' : 'disabled'}>${icon('link')}绑定所选</button>
      </div>
      <div class="hint" style="margin-top:6px">
        ${freeAccounts.length
          ? '绑定后该微信用户即可在微信里操作这个账号；解绑不会删除面板账号。'
          : '所有面板账号都已绑定。如需换绑，先在下方解除某个绑定。'}
      </div>
    </div>
  `;

  wireOwners(ctx, root, host, body, users, byOwner, accounts);
}

function ownersRowsHTML(users, byOwner, accounts, q = '') {
  const ql = (q || '').trim().toLowerCase();
  const list = users.filter((u) => {
    if (!ql) return true;
    const hay = [u.user_id, ...(byOwner[u.user_id] || []).map((b) => {
      const a = accounts.find((x) => x.id === b.account_id) || {};
      return [a.name, a.nickname, a.seq, b.account_id].join(' ');
    })].join(' ').toLowerCase();
    return hay.includes(ql);
  });
  if (!list.length) return `<div class="hint" style="padding:14px 0">没有匹配的用户。</div>`;

  return list.map((u) => {
    const rows = (byOwner[u.user_id] || []).map((b) => {
      const a = accounts.find((x) => x.id === b.account_id) || {};
      const name = a.nickname || a.name || `账号 #${b.account_id}`;
      const warn = a.status && a.status !== 'active' ? ' <span style="color:var(--amber)">⚠️登录失效</span>' : '';
      return `<div class="bind-row">
        <span class="badge neutral mono">#${escapeHtml(String(a.seq ?? b.account_id))}</span>
        <span class="bind-name">${escapeHtml(name)}${warn}</span>
        <button class="btn btn-sm btn-ghost" data-unbind="${b.account_id}" data-user="${escapeHtml(u.user_id)}">解绑</button>
      </div>`;
    }).join('');

    return `<div class="owner-card" data-owner="${escapeHtml(u.user_id)}">
      <div class="owner-head">
        <span class="badge ${u.is_admin ? 'ok' : 'neutral'}">${u.is_admin ? '管理员' : '用户'}</span>
        <span class="owner-id">${escapeHtml(u.user_id)}</span>
        <span class="hint">最近 ${relTime(u.last_seen, state.tz)}</span>
        <div class="owner-actions">
          <button class="btn btn-sm" data-admin="${escapeHtml(u.user_id)}" data-on="${u.is_admin ? '0' : '1'}">${u.is_admin ? '取消管理员' : '设为管理员'}</button>
          <button class="btn btn-sm btn-danger" data-del-user="${escapeHtml(u.user_id)}">${icon('trash')}删除</button>
        </div>
      </div>
      <div class="owner-binds">
        ${rows || '<div class="owner-empty">尚未绑定微信读书账号</div>'}
      </div>
    </div>`;
  }).join('');
}

function wireOwners(ctx, root, host, body, users, byOwner, accounts) {
  const rowsHost = body.querySelector('[data-owner-rows]');
  const redraw = () => {
    rowsHost.innerHTML = ownersRowsHTML(users, byOwner, accounts, ownersQuery);
    wireRowActions();
  };

  const wireRowActions = () => {
    $$('[data-admin]', rowsHost).forEach((btn) => {
      btn.addEventListener('click', async () => {
        setLoading(btn, true);
        try {
          await api.wxBotUserAdmin(btn.dataset.admin, btn.dataset.on === '1');
          toast('权限已更新', 'ok');
          await refreshData(ctx, root);
        } catch (e) { toast(e.message, 'error'); }
        finally { setLoading(btn, false); }
      });
    });

    $$('[data-del-user]', rowsHost).forEach((btn) => {
      btn.addEventListener('click', async () => {
        const ok = await confirmDialog({
          title: '删除微信用户',
          message: '删除该微信用户及其全部账号绑定？面板账号不会被删除。',
          confirmText: '删除',
          danger: true,
        });
        if (!ok) return;
        setLoading(btn, true);
        try {
          await api.wxBotUserDelete(btn.dataset.delUser);
          toast('已删除', 'ok');
          await refreshData(ctx, root);
        } catch (e) { toast(e.message, 'error'); }
        finally { setLoading(btn, false); }
      });
    });

    $$('[data-unbind]', rowsHost).forEach((btn) => {
      btn.addEventListener('click', async () => {
        const ok = await confirmDialog({
          title: '解除绑定',
          message: '解除后该微信用户将不能再通过微信操作这个账号，面板账号仍保留。',
          confirmText: '解绑',
          danger: true,
        });
        if (!ok) return;
        setLoading(btn, true);
        try {
          await api.wxBotUnbind(Number(btn.dataset.unbind), btn.dataset.user);
          toast('已解绑', 'ok');
          await refreshData(ctx, root);
        } catch (e) { toast(e.message, 'error'); }
        finally { setLoading(btn, false); }
      });
    });
  };

  wireRowActions();

  body.querySelector('[data-owner-search]')?.addEventListener('input', (e) => {
    ownersQuery = e.target.value;
    redraw();
  });

  body.querySelector('[data-bind-btn]')?.addEventListener('click', async (e) => {
    const btn = e.currentTarget;
    const uid = body.querySelector('[data-bind-uid]').value;
    const accId = Number(body.querySelector('[data-bind-acc]').value || 0);
    if (!uid || !accId) { toast('请选择用户与账号', 'warn'); return; }
    setLoading(btn, true);
    try {
      await api.wxBotBind(uid, accId);
      toast('已绑定', 'ok');
      await refreshData(ctx, root);
    } catch (e2) { toast(e2.message, 'error'); }
    finally { setLoading(btn, false); }
  });
}

// ---------- 对话记录 ----------

function resetChatLive() {
  chatLive = null;
  chatSeq++;
}

async function renderChatTab(ctx, host) {
  resetChatLive();
  host.innerHTML = `<article class="card">
    <div class="card-head"><div><div class="card-title">${icon('users')}对话记录</div><div class="card-sub">用户与机器人的私聊往来，实时更新</div></div></div>
    <div class="tool-row" style="margin-bottom:10px">
      <select class="select" data-chat-user style="flex:1;min-width:220px"><option value="">加载中…</option></select>
      <button class="btn btn-sm" data-chat-refresh>${icon('refresh')}刷新</button>
    </div>
    <div class="chat-log" data-chat-log><div class="hint">加载中…</div></div>
    <div style="margin-top:8px;display:none" data-chat-more-holder>
      <button class="btn btn-sm" data-chat-more>${icon('download')}加载更早的记录</button>
    </div>
  </article>`;

  const select = host.querySelector('[data-chat-user]');
  const logBox = host.querySelector('[data-chat-log]');
  const moreHolder = host.querySelector('[data-chat-more-holder]');
  const refreshBtn = host.querySelector('[data-chat-refresh]');
  const moreBtn = host.querySelector('[data-chat-more]');

  let data;
  try {
    data = await api.wxBotUsers();
  } catch (e) {
    select.innerHTML = '<option value="">加载失败</option>';
    logBox.innerHTML = `<div class="note warn">${icon('alert')}<div>${escapeHtml(e.message)}</div></div>`;
    return;
  }
  if (!ctx.isActive()) return;

  const users = data.users || [];
  if (!users.length) {
    select.innerHTML = '<option value="">尚无微信用户</option>';
    logBox.innerHTML = '<div class="hint">绑定机器人后，用户发消息会显示在这里。</div>';
    return;
  }
  if (!users.some((u) => u.user_id === chatUser)) chatUser = users[0].user_id;
  select.innerHTML = users.map((u) =>
    `<option value="${escapeHtml(u.user_id)}" ${u.user_id === chatUser ? 'selected' : ''}>${escapeHtml(u.user_id)}${u.is_admin ? ' · 管理员' : ''}</option>`).join('');

  chatOldest = 0;
  logBox.dataset.user = chatUser;
  logBox.innerHTML = '<div class="hint">加载中…</div>';
  chatLive = { user: chatUser, logBox, moreHolder, seen: new Set() };
  await loadChat(ctx, logBox, moreHolder, { reset: true });

  select.addEventListener('change', async () => {
    chatUser = select.value;
    chatOldest = 0;
    logBox.dataset.user = chatUser;
    logBox.innerHTML = '<div class="hint">加载中…</div>';
    chatLive = { user: chatUser, logBox, moreHolder, seen: new Set() };
    await loadChat(ctx, logBox, moreHolder, { reset: true });
  });

  refreshBtn?.addEventListener('click', () => {
    chatOldest = 0;
    logBox.dataset.user = chatUser;
    logBox.innerHTML = '<div class="hint">加载中…</div>';
    chatOldest = 0;
    chatLive = { user: chatUser, logBox, moreHolder, seen: new Set() };
    loadChat(ctx, logBox, moreHolder, { reset: true });
  });

  moreBtn?.addEventListener('click', () => loadChat(ctx, logBox, moreHolder, { reset: false }));
}

async function loadChat(ctx, logBox, moreHolder, { reset }) {
  const uid = chatUser;
  if (!uid) return;
  const seq = ++chatSeq;
  const moreBtn = moreHolder?.querySelector('[data-chat-more]');
  if (moreBtn) setLoading(moreBtn, true);
  try {
    const res = await api.wxBotMessages(uid, { beforeId: reset ? 0 : chatOldest, limit: 50 });
    if (seq !== chatSeq || logBox.dataset.user !== uid || !ctx.isActive()) return;
    const msgs = (res.messages || []).slice().reverse();
    const seen = chatLive?.user === uid ? chatLive.seen : null;
    if (!msgs.length) {
      if (reset) logBox.innerHTML = '<div class="hint">该用户还没有与机器人产生对话。</div>';
      if (moreHolder) moreHolder.style.display = 'none';
      return;
    }
    if (seen) msgs.forEach((m) => seen.add(m.id));
    const html = msgs.map((m) => bubbleHTML(m, state.tz)).join('');
    if (reset) {
      logBox.innerHTML = html;
      logBox.scrollTop = logBox.scrollHeight;
    } else {
      const prevScroll = logBox.scrollTop;
      const prevHeight = logBox.scrollHeight;
      logBox.insertAdjacentHTML('afterbegin', html);
      logBox.scrollTop = logBox.scrollHeight - prevHeight + prevScroll;
    }
    chatOldest = msgs[0]?.id || chatOldest;
    if (moreHolder) moreHolder.style.display = res.has_more ? '' : 'none';
  } catch (e) {
    toast(e.message, 'error');
  } finally {
    if (moreBtn) setLoading(moreBtn, false);
  }
}

function onBotMessage(m) {
  if (!m || !chatLive || m.user_id !== chatLive.user) return;
  if (chatLive.seen.has(m.id)) return;
  chatLive.seen.add(m.id);
  const box = chatLive.logBox;
  if (!box || !box.isConnected) return;
  box.querySelector('.hint')?.remove();
  const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 60;
  box.insertAdjacentHTML('beforeend', bubbleHTML(m, state.tz));
  if (atBottom) box.scrollTop = box.scrollHeight;
}

function bubbleHTML(m, tz) {
  const tag = m.direction === 'in' ? '用户' : '机器人';
  let prefix = '';
  if (m.kind === 'image') prefix = '🖼 ';
  else if (m.kind === 'media') prefix = '📎 ';
  else if (m.kind && m.kind !== 'text') prefix = `[${m.kind}] `;
  const meta = escapeHtml(fmtTime(m.ts, tz));
  return `
    <div class="chat-row ${m.direction === 'in' ? 'in' : 'out'}">
      <div class="chat-bubble"><b class="chat-tag">${tag}</b>${prefix}${escapeHtml(m.content || '')}</div>
      <div class="chat-meta">${meta}</div>
    </div>`;
}
