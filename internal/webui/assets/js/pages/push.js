import { api } from '../api.js';
import { state, refreshChannels } from '../state.js';
import { icon } from '../icons.js';
import { pushChannelDialog } from '../components.js';
import { renderError, skeletonHTML, bindEnabledToggle } from '../helpers.js';
import { $, $$, escapeHtml, toast, setLoading, confirmDialog, pageHeaderHTML } from '../util.js';

const TYPES = {
  pushplus: { label: 'PushPlus', icon: 'megaphone', color: 'var(--amber)' },
  wxpusher: { label: 'WxPusher', icon: 'chat', color: 'var(--blue)' },
  telegram: { label: 'Telegram', icon: 'plane', color: 'var(--purple)' },
  serverchan: { label: 'ServerChan', icon: 'bell', color: 'var(--accent)' },
  serverchan3: { label: 'Server酱³', icon: 'zap', color: 'var(--accent)' },
  bark: { label: 'Bark', icon: 'moon', color: 'var(--amber)' },
  ntfy: { label: 'Ntfy', icon: 'globe', color: 'var(--blue)' },
  gotify: { label: 'Gotify', icon: 'server', color: 'var(--purple)' },
  pushdeer: { label: 'PushDeer', icon: 'rocket', color: 'var(--amber)' },
  feishu: { label: '飞书', icon: 'chatDuo', color: 'var(--blue)' },
  wework: { label: '企业微信', icon: 'building', color: 'var(--purple)' },
  dingtalk: { label: '钉钉', icon: 'pin', color: 'var(--blue)' },
  apprise: { label: 'Apprise', icon: 'external', color: 'var(--amber)' },
};

function mask(value) {
  const v = String(value || '');
  if (v.length <= 6) return v ? '••••••' : '未配置';
  return `${v.slice(0, 4)}••••${v.slice(-2)}`;
}

function summary(ch) {
  const c = ch.config || {};
  switch (ch.type) {
    case 'pushplus': return `token ${mask(c.token)}`;
    case 'wxpusher': return `SPT ${mask(c.spt)}`;
    case 'telegram': return `chat ${escapeHtml(c.chat_id || '—')} · ${mask(c.bot_token)}`;
    case 'serverchan': return `SendKey ${mask(c.sendkey)}`;
    case 'serverchan3': return `uid ${escapeHtml(c.uid || '—')} · key ${mask(c.sendkey)}`;
    case 'bark': return `key ${mask(c.device_key)}`;
    case 'ntfy': return `topic ${escapeHtml(c.topic || '—')}`;
    case 'gotify': return `token ${mask(c.token)}`;
    case 'pushdeer': return `key ${mask(c.pushkey)}`;
    case 'feishu': case 'wework': case 'dingtalk': return `webhook ${mask(c.webhook_url)}`;
    case 'apprise': return `token ${mask(c.token)}`;
    default: return '';
  }
}

export async function renderPush(ctx) {
  const { root } = ctx;
  root.innerHTML = skeletonHTML(['height:70px;width:45%', 'height:200px']);

  try {
    await refreshChannels();
  } catch (e) {
    // 先守卫后渲染：路由已切换时旧请求的失败响应不写共享 root。
    if (!ctx.isActive()) return;
    renderError(root, e, { onRetry: () => ctx.reload() });
    return;
  }
  if (!ctx.isActive()) return;

  const list = state.channels;
  root.innerHTML = `
    ${pageHeaderHTML({
      title: '推送渠道',
      sub: `${list.length} 个渠道 · ${list.filter((c) => c.enabled).length} 个启用中`,
      actions: `<button class="btn btn-primary" id="btn-add">${icon('plus')}<span>添加渠道</span></button>`,
    })}

    ${list.length ? `<div class="table-wrap"><table class="table push-table table-wide">
      <thead><tr>
        <th style="min-width:220px">渠道</th>
        <th style="min-width:260px">类型 / 凭据</th>
        <th style="width:130px">是否启用</th>
        <th style="width:220px">操作</th>
      </tr></thead>
      <tbody>${list.map(card).join('')}</tbody>
    </table></div>` : `
      <article class="card"><div class="empty" style="padding:54px 20px">
        <div class="empty-ico" style="width:60px;height:60px;border-radius:18px">${icon('bell')}</div>
        <h3>还没有推送渠道</h3>
        <p>阅读重试后仍失败或确认登录失效时，可通过微信、Telegram 等渠道收到提醒。</p>
        <button class="btn btn-primary" id="empty-add">${icon('plus')}添加第一个渠道</button>
      </div></article>`}

    <div class="note" style="margin-top:16px">${icon('shield')}
      <div>推送配置（Token、SPT 等）使用本机密钥加密存储。测试消息会立即发送，请确认渠道配置正确。</div>
    </div>
  `;
  if (!ctx.isActive()) return;

  const openAdd = () => pushChannelDialog({ onDone: () => ctx.reload() });
  $('#btn-add', root)?.addEventListener('click', openAdd);
  $('#empty-add', root)?.addEventListener('click', openAdd);

  $$('[data-edit]', root).forEach((btn) => btn.addEventListener('click', () => {
    const ch = state.channels.find((c) => c.id === Number(btn.dataset.edit));
    if (!ch) return;
    pushChannelDialog({ channel: ch, onDone: () => ctx.reload() });
  }));

  $$('[data-del]', root).forEach((btn) => btn.addEventListener('click', async () => {
    const ch = state.channels.find((c) => c.id === Number(btn.dataset.del));
    if (!ch) return;
    const ok = await confirmDialog({
      title: '删除推送渠道',
      message: `确定删除「${ch.name}」吗？使用该渠道的账号将不再推送。`,
      confirmText: '删除',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deletePush(ch.id);
      toast('渠道已删除', 'ok');
      ctx.reload();
    } catch (e) {
      toast(e.message, 'error');
    }
  }));

  $$('[data-test]', root).forEach((btn) => btn.addEventListener('click', async () => {
    setLoading(btn, true);
    try {
      await api.testPush(Number(btn.dataset.test));
      toast('测试消息已发送，请检查接收端', 'ok');
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      setLoading(btn, false);
    }
  }));

  bindEnabledToggle($$('[data-toggle]', root), {
    call: (input) => api.updatePush(Number(input.dataset.toggle), { enabled: input.checked }),
    labels: { on: '渠道已启用', off: '渠道已停用' },
    onSuccess: (input) => {
      const ch = state.channels.find((c) => c.id === Number(input.dataset.toggle));
      if (ch) ch.enabled = input.checked;
    },
  });
}

function card(ch) {
  const meta = TYPES[ch.type] || { label: ch.type, icon: 'bell', color: 'var(--text-2)' };
  return `<tr>
    <td>
      <div style="display:flex;align-items:center;gap:10px;justify-content:center">
        <div class="avatar" style="width:38px;height:38px;border-radius:11px;color:${meta.color}">${icon(meta.icon)}</div>
        <div style="min-width:0;text-align:left">
          <div class="cell-strong">${escapeHtml(ch.name)}</div>
          <div class="cell-muted">${meta.label}</div>
        </div>
      </div>
    </td>
    <td class="cell-muted">${summary(ch) || '—'}</td>
    <td>
      <label class="switch" title="${ch.enabled ? '停用渠道' : '启用渠道'}">
        <input type="checkbox" data-toggle="${ch.id}" ${ch.enabled ? 'checked' : ''} aria-label="${ch.enabled ? '停用渠道' : '启用渠道'}"><i></i>
      </label>
      <div class="hint" style="margin-top:4px">${ch.enabled ? '启用中，可收推送' : '已停用'}</div>
    </td>
    <td>
      <div style="display:flex;gap:7px;justify-content:center">
        <button class="btn btn-sm" data-test="${ch.id}">${icon('bell')}测试</button>
        <button class="btn btn-sm btn-ghost" data-edit="${ch.id}">${icon('edit')}编辑</button>
        <button class="btn btn-sm btn-ghost btn-icon" data-del="${ch.id}" title="删除" aria-label="删除渠道">${icon('trash')}</button>
      </div>
    </td>
  </tr>`;
}
