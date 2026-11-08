import { api } from '../api.js';
import { state } from '../state.js';
import { icon } from '../icons.js';
import { redrawCharts } from '../charts.js';
import { renderError, skeletonHTML } from '../helpers.js';
import {
  $, $$, escapeHtml, fmtClock, toast, setLoading, themePreference, applyTheme, resolveTheme,
  pageHeaderHTML, confirmDialog,
} from '../util.js';

const TZ_OPTIONS = [
  ['Asia/Shanghai', '中国标准时间（Asia/Shanghai）'],
  ['Asia/Hong_Kong', '香港（Asia/Hong_Kong）'],
  ['Asia/Taipei', '台北（Asia/Taipei）'],
  ['Asia/Tokyo', '东京（Asia/Tokyo）'],
  ['Asia/Singapore', '新加坡（Asia/Singapore）'],
  ['Europe/London', '伦敦（Europe/London）'],
  ['America/New_York', '纽约（America/New_York）'],
  ['UTC', 'UTC'],
];

let infoCache = null;

export async function renderSettings(ctx) {
  const { root } = ctx;
  root.innerHTML = skeletonHTML(['height:44px;width:40%', 'height:280px']);

  let info;
  try {
    info = await api.settings();
  } catch (e) {
    renderError(root, e, { onRetry: () => ctx.reload() });
    return;
  }
  if (!ctx.isActive()) return;
  infoCache = info;

  root.innerHTML = `
    ${pageHeaderHTML({
      title: '设置',
    })}
    <div class="settings-body"></div>
  `;

  draw(ctx, root);
}

function draw(ctx, root, info = infoCache) {
  const host = $('.settings-body', root);
  host.innerHTML = siteHTML() + securityHTML(info);

  wireSite(ctx, host, info);
  wireSecurity(ctx, host, info);
}

/* ---------- 外观与站点 ---------- */

function siteHTML() {
  const info = infoCache;
  return `
    <div class="settings-grid" style="margin-bottom:16px">
      <div>
        <article class="card" style="margin-bottom:16px">
          <div class="card-head"><div><div class="card-title">${icon('sun')}外观</div><div class="card-sub">主题会记忆在本地浏览器</div></div></div>
          <div class="type-grid" style="grid-template-columns:repeat(3,1fr)">
            ${[['dark', '深色', 'moon'], ['light', '浅色', 'sun'], ['system', '跟随系统', 'settings']].map(([v, label, ic]) => `
              <button class="type-opt ${themePreference() === v ? 'active' : ''}" data-theme-pref="${v}"><b>${label}</b>${icon(ic)}</button>`).join('')}
          </div>
          <div class="hint">当前生效：${resolveTheme(themePreference()) === 'dark' ? '深色' : '浅色'}</div>
        </article>
      </div>

      <div>
        <article class="card">
          <div class="card-head"><div><div class="card-title">${icon('globe')}站点信息</div><div class="card-sub">影响标题、时区与定时任务</div></div></div>
          <div class="field">
            <label class="label" for="site-name">站点名称</label>
            <input class="input" id="site-name" value="${escapeHtml(info.site_name)}" maxlength="40">
          </div>
          <div class="field">
            <label class="label" for="site-tz">时区</label>
            <input class="input" id="site-tz" list="tz-list" value="${escapeHtml(info.timezone)}">
            <datalist id="tz-list">
              ${TZ_OPTIONS.map(([v, label]) => `<option value="${v}">${label}</option>`).join('')}
            </datalist>
            <div class="hint">用于统计「今天」与定时任务的触发时间。修改后会立即重建定时任务。</div>
          </div>
          <button class="btn btn-primary" id="save-site">${icon('check')}保存站点设置</button>
        </article>
      </div>
    </div>
  `;
}

function wireSite(ctx, host, info) {
  $$('[data-theme-pref]', host).forEach((btn) => btn.addEventListener('click', () => {
    const v = btn.dataset.themePref;
    applyTheme(v);
    $$('[data-theme-pref]', host).forEach((b) => b.classList.toggle('active', b === btn));
    redrawCharts();
    toast(`已切换为${v === 'system' ? '跟随系统' : v === 'dark' ? '深色' : '浅色'}主题`, 'ok');
  }));

  $('#save-site', host)?.addEventListener('click', async (e) => {
    const name = $('#site-name', host).value.trim();
    const tz = $('#site-tz', host).value.trim();
    if (!name) { toast('站点名称不能为空', 'warn'); return; }
    setLoading(e.currentTarget, true);
    try {
      const res = await api.updateSettings({ site_name: name, timezone: tz });
      state.tz = res.timezone;
      state.status = { ...(state.status || {}), site_name: name };
      document.title = `设置 · ${name}`;
      const brand = document.querySelector('.brand-name');
      if (brand) brand.textContent = name;
      // 就地更新缓存，不整页刷新。
      infoCache = { ...infoCache, site_name: name, timezone: res.timezone };
      toast('站点设置已保存', 'ok');
    } catch (e2) {
      toast(e2.message, 'error');
    } finally {
      setLoading(e.currentTarget, false);
    }
  });
}

/* ---------- 安全、检测与运行信息 ---------- */

function securityHTML(info) {
  return `
    <div class="settings-grid">
      <div>
        <article class="card" style="margin-bottom:16px">
          <div class="card-head"><div><div class="card-title">${icon('shield')}账号状态检测</div><div class="card-sub">后台自动校验各账号登录态</div></div></div>
          <div class="field">
            <label class="label" for="check-interval">自动检测间隔</label>
            <select class="select" id="check-interval">
              ${[[0, '关闭自动检测'], [15, '每 15 分钟'], [30, '每 30 分钟'], [60, '每 1 小时'], [120, '每 2 小时'], [240, '每 4 小时'], [720, '每 12 小时']]
                .map(([v, label]) => `<option value="${v}" ${Number(info.check_interval ?? 60) === v ? 'selected' : ''}>${label}</option>`).join('')}
            </select>
            <div class="hint">正在阅读的账号会跳过检测。检测到账号失效时，绑定了推送渠道的账号会收到「账号失效」提醒。</div>
          </div>
          <button class="btn btn-primary" id="save-check">${icon('check')}保存检测设置</button>
        </article>

        <article class="card">
          <div class="card-head"><div><div class="card-title">${icon('key')}修改密码</div><div class="card-sub">修改后其他设备的登录会话会失效</div></div></div>
          <div class="field">
            <label class="label" for="pw-current">当前密码</label>
            <input class="input" id="pw-current" type="password" autocomplete="current-password">
          </div>
          <div class="grid-2">
            <div class="field">
              <label class="label" for="pw-new">新密码</label>
              <input class="input" id="pw-new" type="password" autocomplete="new-password" placeholder="至少 6 位">
            </div>
            <div class="field">
              <label class="label" for="pw-confirm">确认新密码</label>
              <input class="input" id="pw-confirm" type="password" autocomplete="new-password">
            </div>
          </div>
          <button class="btn btn-primary" id="save-pw">${icon('shield')}更新密码</button>
        </article>
      </div>

      <div>
        <article class="card" style="margin-bottom:16px">
          <div class="card-head"><div><div class="card-title">${icon('server')}运行信息</div><div class="card-sub">当前服务实例</div></div></div>
          <div class="kv"><span>版本</span><b>${escapeHtml(info.version)}</b></div>
          <div class="kv"><span>Go 运行时</span><b>${escapeHtml(info.go_version)}</b></div>
          <div class="kv"><span>已运行</span><b>${Math.floor(info.uptime_seconds / 3600)} 小时 ${Math.floor((info.uptime_seconds % 3600) / 60)} 分</b></div>
          <div class="kv"><span>启动于</span><b>${fmtClock(info.started_at, info.timezone)}</b></div>
          <div class="kv"><span>时区</span><b>${escapeHtml(info.timezone)}</b></div>
        </article>

        <article class="card">
          <div class="card-head"><div><div class="card-title">${icon('shield')}安全与隐私</div></div></div>
          <div class="note" style="margin-bottom:10px">${icon('shield')}<div>账号 Cookie 与推送 Token 使用 AES-256-GCM 加密后保存在本机 SQLite，密钥文件位于数据目录，不会上传。</div></div>
          <div class="note">${icon('alert')}<div>本工具通过模拟阅读心跳保持时长，可能违反微信读书用户协议并触发风控。请仅用于个人学习，自行承担账号风险。</div></div>
          <div class="bot-card-actions">
            <button class="btn btn-danger" id="logout-panel">${icon('logout')}退出登录</button>
          </div>
        </article>
      </div>
    </div>
  `;
}

function wireSecurity(ctx, host, info) {
  $('#save-check', host)?.addEventListener('click', async (e) => {
    const minutes = Number($('#check-interval', host).value);
    setLoading(e.currentTarget, true);
    try {
      await api.updateSettings({ check_interval_minutes: minutes });
      infoCache = { ...infoCache, check_interval: minutes };
      toast(minutes === 0 ? '已关闭自动检测' : `自动检测已设为每 ${minutes} 分钟`, 'ok');
    } catch (e2) {
      toast(e2.message, 'error');
    } finally {
      setLoading(e.currentTarget, false);
    }
  });

  $('#save-pw', host)?.addEventListener('click', async (e) => {
    const current = $('#pw-current', host).value;
    const next = $('#pw-new', host).value;
    const confirm = $('#pw-confirm', host).value;
    if (!current || !next) { toast('请填写当前密码与新密码', 'warn'); return; }
    if (next.length < 6) { toast('新密码至少 6 位', 'warn'); return; }
    if (next !== confirm) { toast('两次输入的新密码不一致', 'warn'); return; }
    setLoading(e.currentTarget, true);
    try {
      await api.changePassword(current, next);
      toast('密码已更新，其他会话已失效', 'ok');
      $('#pw-current', host).value = '';
      $('#pw-new', host).value = '';
      $('#pw-confirm', host).value = '';
    } catch (e2) {
      toast(e2.message, 'error');
    } finally {
      setLoading(e.currentTarget, false);
    }
  });

  $('#logout-panel', host)?.addEventListener('click', async () => {
    const ok = await confirmDialog({
      title: '退出登录',
      message: '退出后需要重新输入管理密码才能进入面板。',
      confirmText: '退出',
      danger: true,
    });
    if (!ok) return;
    try { await api.logout(); } catch { /* ignore */ }
    location.reload();
  });
}
