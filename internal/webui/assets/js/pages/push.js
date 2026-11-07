import { api } from '../api.js';
import { state, refreshChannels } from '../state.js';
import { icon } from '../icons.js';
import { pushChannelDialog } from '../components.js';
import { $, $$, escapeHtml, toast, setLoading, confirmDialog } from '../util.js';

const TYPES = {
  pushplus: { label: 'PushPlus', icon: 'bell', color: 'var(--amber)' },
  wxpusher: { label: 'WxPusher', icon: 'bell', color: 'var(--blue)' },
  telegram: { label: 'Telegram', icon: 'external', color: 'var(--purple)' },
  serverchan: { label: 'ServerChan', icon: 'bell', color: 'var(--accent)' },
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
    default: return '';
  }
}

export async function renderPush(ctx) {
  const { root } = ctx;
  root.innerHTML = `<div class="page-loading"><div class="skeleton" style="height:70px;width:45%"></div><div class="skeleton" style="height:200px"></div></div>`;

  try {
    await refreshChannels();
  } catch (e) {
    root.innerHTML = `<div class="card"><div class="empty"><div class="empty-ico">${icon('alert')}</div><h3>加载失败</h3><p>${escapeHtml(e.message)}</p></div></div>`;
    return;
  }

  const list = state.channels;
  root.innerHTML = `
    <div class="topbar">
      <div>
        <div class="page-kicker">推送</div>
        <h1 class="page-title">阅读结果，及时送达</h1>
        <div class="page-sub">${list.length ? `已配置 ${list.length} 个渠道 · ${list.filter((c) => c.enabled).length} 个启用中` : '支持 PushPlus、WxPusher、Telegram、ServerChan'}</div>
      </div>
      <div class="top-actions">
        <button class="btn btn-primary" id="btn-add">${icon('plus')}<span>添加渠道</span></button>
      </div>
    </div>

    ${list.length ? `<div class="accounts-grid">${list.map(card).join('')}</div>` : `
      <article class="card"><div class="empty" style="padding:54px 20px">
        <div class="empty-ico" style="width:60px;height:60px;border-radius:18px">${icon('bell')}</div>
        <h3>还没有推送渠道</h3>
        <p>阅读完成后，把本次时长与状态推送到微信或 Telegram，随时掌握运行结果。</p>
        <button class="btn btn-primary" id="empty-add">${icon('plus')}添加第一个渠道</button>
      </div></article>`}

    <div class="note" style="margin-top:16px">${icon('shield')}
      <div>推送配置（Token、SPT 等）使用本机密钥加密存储。测试消息会立即发送，请确认渠道配置正确。</div>
    </div>
  `;

  const openAdd = () => pushChannelDialog({ onDone: () => ctx.reload() });
  $('#btn-add', root)?.addEventListener('click', openAdd);
  $('#empty-add', root)?.addEventListener('click', openAdd);

  $$('[data-edit]', root).forEach((btn) => btn.addEventListener('click', () => {
    const ch = state.channels.find((c) => c.id === Number(btn.dataset.edit));
    pushChannelDialog({ channel: ch, onDone: () => ctx.reload() });
  }));

  $$('[data-del]', root).forEach((btn) => btn.addEventListener('click', async () => {
    const ch = state.channels.find((c) => c.id === Number(btn.dataset.del));
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

  $$('[data-toggle]', root).forEach((input) => input.addEventListener('change', async () => {
    const ch = state.channels.find((c) => c.id === Number(input.dataset.toggle));
    try {
      await api.updatePush(ch.id, { enabled: input.checked });
      toast(input.checked ? '渠道已启用' : '渠道已停用', 'ok');
      ch.enabled = input.checked;
    } catch (e) {
      input.checked = !input.checked;
      toast(e.message, 'error');
    }
  }));
}

function card(ch) {
  const meta = TYPES[ch.type] || { label: ch.type, icon: 'bell', color: 'var(--text-2)' };
  return `<article class="account-card">
    <div class="account-top">
      <div class="avatar" style="color:${meta.color}">${icon(meta.icon)}</div>
      <div style="flex:1;min-width:0">
        <div class="account-name">${escapeHtml(ch.name)}
          ${ch.enabled ? '<span class="badge ok">启用中</span>' : '<span class="badge neutral">已停用</span>'}
        </div>
        <div class="account-sub">${meta.label} · ${summary(ch)}</div>
      </div>
      <label class="switch" title="${ch.enabled ? '停用' : '启用'}">
        <input type="checkbox" data-toggle="${ch.id}" ${ch.enabled ? 'checked' : ''}><i></i>
      </label>
    </div>
    <div class="account-actions">
      <button class="btn btn-sm" data-test="${ch.id}">${icon('bell')}测试</button>
      <button class="btn btn-sm btn-ghost" data-edit="${ch.id}">${icon('edit')}编辑</button>
      <button class="btn btn-sm btn-ghost btn-icon" data-del="${ch.id}" title="删除">${icon('trash')}</button>
    </div>
  </article>`;
}
