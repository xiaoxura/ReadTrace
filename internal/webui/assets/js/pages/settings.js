import { api } from '../api.js';
import { state } from '../state.js';
import { icon } from '../icons.js';
import { redrawCharts } from '../charts.js';
import {
  $, $$, escapeHtml, fmtClock, toast, setLoading, themePreference, applyTheme, resolveTheme,
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

export async function renderSettings(ctx) {
  const { root } = ctx;
  root.innerHTML = `<div class="page-loading"><div class="skeleton" style="height:70px;width:40%"></div><div class="skeleton" style="height:280px"></div></div>`;

  let info;
  try {
    info = await api.settings();
  } catch (e) {
    root.innerHTML = `<div class="card"><div class="empty"><div class="empty-ico">${icon('alert')}</div><h3>加载失败</h3><p>${escapeHtml(e.message)}</p></div></div>`;
    return;
  }
  const pref = themePreference();

  root.innerHTML = `
    <div class="topbar">
      <div>
        <div class="page-kicker">设置</div>
        <h1 class="page-title">面板设置</h1>
        <div class="page-sub">外观、站点信息与安全</div>
      </div>
    </div>

    <div class="dash-grid" style="align-items:start">
      <div>
        <article class="card" style="margin-bottom:16px">
          <div class="card-head"><div><div class="card-title">${icon('sun')}外观</div><div class="card-sub">主题会记忆在本地浏览器</div></div></div>
          <div class="type-grid" style="grid-template-columns:repeat(3,1fr)">
            ${[['dark', '深色', 'moon'], ['light', '浅色', 'sun'], ['system', '跟随系统', 'settings']].map(([v, label, ic]) => `
              <button class="type-opt ${pref === v ? 'active' : ''}" data-theme-pref="${v}"><b>${label}</b>${icon(ic)}</button>`).join('')}
          </div>
          <div class="hint">当前生效：${resolveTheme(pref) === 'dark' ? '深色' : '浅色'}</div>
        </article>

        <article class="card" style="margin-bottom:16px">
          <div class="card-head"><div><div class="card-title">${icon('globe')}站点信息</div><div class="card-sub">影响标题、时区与定时任务</div></div></div>
          <div class="field">
            <label class="label">站点名称</label>
            <input class="input" id="site-name" value="${escapeHtml(info.site_name)}" maxlength="40">
          </div>
          <div class="field">
            <label class="label">时区</label>
            <input class="input" id="site-tz" list="tz-list" value="${escapeHtml(info.timezone)}">
            <datalist id="tz-list">
              ${TZ_OPTIONS.map(([v, label]) => `<option value="${v}">${label}</option>`).join('')}
            </datalist>
            <div class="hint">用于统计「今天」与定时任务的触发时间。修改后会立即重建定时任务。</div>
          </div>
          <button class="btn btn-primary" id="save-site">${icon('check')}保存站点设置</button>
        </article>

        <article class="card">
          <div class="card-head"><div><div class="card-title">${icon('key')}修改密码</div><div class="card-sub">修改后其他设备的登录会话会失效</div></div></div>
          <div class="field">
            <label class="label">当前密码</label>
            <input class="input" id="pw-current" type="password" autocomplete="current-password">
          </div>
          <div class="grid-2">
            <div class="field">
              <label class="label">新密码</label>
              <input class="input" id="pw-new" type="password" autocomplete="new-password" placeholder="至少 6 位">
            </div>
            <div class="field">
              <label class="label">确认新密码</label>
              <input class="input" id="pw-confirm" type="password" autocomplete="new-password">
            </div>
          </div>
          <button class="btn btn-primary" id="save-pw">${icon('shield')}更新密码</button>
        </article>
      </div>

      <div>
        <article class="card" style="margin-bottom:16px">
          <div class="card-head"><div><div class="card-title">${icon('info')}运行信息</div><div class="card-sub">当前服务实例</div></div></div>
          <div class="kv"><span>版本</span><b>${escapeHtml(info.version)}</b></div>
          <div class="kv"><span>Go 运行时</span><b>${escapeHtml(info.go_version)}</b></div>
          <div class="kv"><span>已运行</span><b>${Math.floor(info.uptime_seconds / 3600)} 小时 ${Math.floor((info.uptime_seconds % 3600) / 60)} 分</b></div>
          <div class="kv"><span>启动于</span><b>${fmtClock(info.started_at, info.timezone)}</b></div>
          <div class="kv"><span>时区</span><b>${escapeHtml(info.timezone)}</b></div>
        </article>

        <article class="card" style="margin-bottom:16px">
          <div class="card-head"><div><div class="card-title">${icon('shield')}安全与隐私</div></div></div>
          <div class="note" style="margin-bottom:10px">${icon('shield')}<div>账号 Cookie 与推送 Token 使用 AES-256-GCM 加密后保存在本机 SQLite，密钥文件位于数据目录，不会上传。</div></div>
          <div class="note warn" style="margin-bottom:14px">${icon('alert')}<div>本工具通过模拟阅读心跳保持时长，可能违反微信读书用户协议并触发风控。请仅用于个人学习，自行承担账号风险。</div></div>
          <button class="btn btn-danger" id="logout">${icon('logout')}退出登录</button>
        </article>

        <article class="card">
          <div class="card-head"><div><div class="card-title">${icon('bookOpen')}关于读时</div></div></div>
          <p class="hint" style="font-size:12px;line-height:1.8">
            读时（ReadPilot）是一个单文件、可自托管的微信读书阅读面板。
            数据全部保存在本地，不需要数据库服务，把二进制和 <code>data/</code> 目录带走即可迁移。
          </p>
          <div class="hint" style="margin-top:12px">构建：<code>go build -o wxread-panel ./cmd/wxread-panel</code></div>
        </article>
      </div>
    </div>
  `;

  $$('[data-theme-pref]', root).forEach((btn) => btn.addEventListener('click', () => {
    const v = btn.dataset.themePref;
    applyTheme(v);
    $$('[data-theme-pref]', root).forEach((b) => b.classList.toggle('active', b === btn));
    redrawCharts();
    toast(`已切换为${v === 'system' ? '跟随系统' : v === 'dark' ? '深色' : '浅色'}主题`, 'ok');
  }));

  $('#save-site', root).addEventListener('click', async (e) => {
    const name = $('#site-name', root).value.trim();
    const tz = $('#site-tz', root).value.trim();
    if (!name) { toast('站点名称不能为空', 'warn'); return; }
    setLoading(e.currentTarget, true);
    try {
      const res = await api.updateSettings({ site_name: name, timezone: tz });
      state.tz = res.timezone;
      document.title = `${name} · ReadPilot`;
      toast('站点设置已保存', 'ok');
      ctx.reload();
    } catch (e2) {
      toast(e2.message, 'error');
    } finally {
      setLoading(e.currentTarget, false);
    }
  });

  $('#save-pw', root).addEventListener('click', async (e) => {
    const current = $('#pw-current', root).value;
    const next = $('#pw-new', root).value;
    const confirm = $('#pw-confirm', root).value;
    if (!current || !next) { toast('请填写当前密码与新密码', 'warn'); return; }
    if (next.length < 6) { toast('新密码至少 6 位', 'warn'); return; }
    if (next !== confirm) { toast('两次输入的新密码不一致', 'warn'); return; }
    setLoading(e.currentTarget, true);
    try {
      await api.changePassword(current, next);
      toast('密码已更新，其他会话已失效', 'ok');
      $('#pw-current', root).value = '';
      $('#pw-new', root).value = '';
      $('#pw-confirm', root).value = '';
    } catch (e2) {
      toast(e2.message, 'error');
    } finally {
      setLoading(e.currentTarget, false);
    }
  });

  $('#logout', root).addEventListener('click', async () => {
    try { await api.logout(); } catch { /* ignore */ }
    location.reload();
  });
}
