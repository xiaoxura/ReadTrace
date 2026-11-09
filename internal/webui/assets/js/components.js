import { api } from './api.js';
import { icon } from './icons.js';
import { state, refreshAccounts, accountById } from './state.js';
import { runStatusBadge } from './helpers.js';
import {
  $, $$, escapeHtml, openModal, confirmDialog, toast, fmtMinutes, fmtClock, fmtTime, humanCron,
  copyText, debounce, setLoading, initials, lockScroll, unlockScroll,
  parseDurationMinutes, setFieldError, clearFieldErrors, focusFirstError,
} from './util.js';

export function avatarHTML(acc, size = 46) {
  if (acc?.avatar) {
    return `<img src="${escapeHtml(acc.avatar)}" alt="" loading="lazy">`;
  }
  return `<span style="font-size:${Math.round(size * 0.38)}px">${escapeHtml(initials(acc?.name))}</span>`;
}

export function accountSelectHTML(selected = 0, { includeAll = false, id = 'account', style = '' } = {}) {
  const opts = state.accounts.map((a) =>
    `<option value="${a.id}" ${Number(selected) === a.id ? 'selected' : ''}>${escapeHtml(a.name)}</option>`).join('');
  const all = includeAll ? `<option value="0" ${!selected ? 'selected' : ''}>全部账号</option>` : '';
  return `<select class="select" id="${id}"${style ? ` style="${escapeHtml(style)}"` : ''}>${all}${opts}</select>`;
}

const MINUTE_PRESETS = [15, 30, 60, 90, 120];

// 今日余量可能不足 1 分钟，不能用一位小数的分钟数显示成 0.0。
function readingTime(seconds) {
  const total = Math.max(0, Math.floor(Number(seconds) || 0));
  const minutes = Math.floor(total / 60);
  const rest = total % 60;
  if (!minutes) return `${rest} 秒`;
  return rest ? `${minutes} 分 ${rest} 秒` : `${minutes} 分钟`;
}

export function startRunDialog({ accountId = 0, onDone } = {}) {
  const accounts = state.accounts;
  if (!accounts.length) {
    toast('请先添加一个微信读书账号', 'warn');
    qrLoginDialog({ onDone });
    return;
  }
  const target = accountById(accountId) || accounts[0];
  const defaultMinutes = target?.default_minutes || 30;
  let closed = false;
  let accountRequest = 0;
  let inputVersion = 0;
  let accountLoading = false;
  let submitting = false;
  let currentAccount = null;
  const { mask, close } = openModal({
    title: '开始阅读',
    subtitle: '按账号心跳间隔上报进度，达到本次或每日目标后结束',
    body: `
      <div class="field">
        <label class="label" for="run-account">账号</label>
        ${accountSelectHTML(target.id, { id: 'run-account' })}
      </div>
      <div class="field">
        <label class="label" for="run-minutes">阅读时长 <span class="opt">分钟</span></label>
        <input class="input" id="run-minutes" type="number" min="1" max="1440" value="${escapeHtml(defaultMinutes)}">
        <div class="chips" style="margin-top:8px">
          ${MINUTE_PRESETS.map((m) => `<button type="button" class="chip ${m === defaultMinutes ? 'active' : ''}" data-min="${m}">${m} 分钟</button>`).join('')}
        </div>
        <div class="hint">账号默认时长可在「账号 → 编辑」中修改。单次最长 24 小时。</div>
      </div>
      <div class="note" data-daily-preview aria-live="polite">${icon('info')}<div>正在读取今日阅读进度…</div></div>
      <div class="note">
        ${icon('info')}
        <div>运行期间请保持服务在线。进度会实时同步到面板，可随时停止。</div>
      </div>`,
    footer: `<button class="btn btn-ghost" data-cancel>取消</button>
      <button class="btn btn-primary" data-start>${icon('play')}开始阅读</button>`,
    onClose: () => { closed = true; accountRequest++; },
  });

  const accountSelect = $('#run-account', mask);
  const minutesInput = $('#run-minutes', mask);
  const preview = $('[data-daily-preview]', mask);
  const btn = $('[data-start]', mask);
  // 与定时任务（缺陷 08）共用同一解析函数：空串/0/越界/非整数都视为无
  // 效，交给 toast 提示，不做静默回退。
  const validMinutes = () => parseDurationMinutes(minutesInput.value);
  const updateChips = () => {
    $$('.chip', mask).forEach((chip) => chip.classList.toggle('active', Number(chip.dataset.min) === validMinutes()));
  };
  const updatePreview = () => {
    if (closed) return;
    const minutes = validMinutes();
    const goal = Number(currentAccount?.daily_goal_minutes) || 0;
    const today = Math.max(0, Number(currentAccount?.today_seconds) || 0);
    const remaining = Math.max(0, Number(currentAccount?.daily_remaining_seconds) || 0);
    let message;
    if (accountLoading) message = '正在读取今日阅读进度…';
    else if (!currentAccount) message = '今日进度暂不可用，服务端仍会按最新目标计算本次时长。';
    else if (!goal) message = `每日目标已关闭 · 今日已读 ${readingTime(today)}。本次按填写时长运行。`;
    else if (!remaining) message = `今日已读 ${readingTime(today)} / 目标 ${readingTime(goal * 60)}，已达标。`;
    else message = `今日已读 ${readingTime(today)} / 目标 ${readingTime(goal * 60)} · 还需 ${readingTime(remaining)}${minutes ? `；本次最多 ${readingTime(Math.min(minutes * 60, remaining))}` : ''}。`;
    preview.innerHTML = `${icon('info')}<div>${escapeHtml(message)}</div>`;
    btn.disabled = submitting || accountLoading || (goal > 0 && remaining === 0);
    btn.innerHTML = goal > 0 && remaining === 0 ? '今日已达标' : `${icon('play')}开始阅读`;
  };
  const loadAccount = async () => {
    const id = Number(accountSelect.value);
    const request = ++accountRequest;
    const version = inputVersion;
    accountLoading = true;
    currentAccount = null;
    updatePreview();
    try {
      const account = await api.account(id);
      if (closed || request !== accountRequest) return;
      currentAccount = account;
      if (version === inputVersion) {
        minutesInput.value = String(account.default_minutes || 30);
        updateChips();
      }
    } catch {
      if (closed || request !== accountRequest) return;
      currentAccount = null;
    } finally {
      if (!closed && request === accountRequest) {
        accountLoading = false;
        updatePreview();
      }
    }
  };
  $('[data-cancel]', mask).addEventListener('click', close);
  $$('.chip', mask).forEach((chip) => chip.addEventListener('click', () => {
    minutesInput.value = chip.dataset.min;
    inputVersion++;
    updateChips();
    updatePreview();
  }));
  minutesInput.addEventListener('input', () => { inputVersion++; updateChips(); updatePreview(); });
  accountSelect.addEventListener('change', () => {
    const selected = accountById(accountSelect.value);
    minutesInput.value = String(selected?.default_minutes || 30);
    inputVersion++;
    updateChips();
    loadAccount();
  });
  loadAccount();
  btn.addEventListener('click', async () => {
    const minutes = validMinutes();
    if (minutes === null) {
      toast('时长需为 1~1440 的整数分钟', 'warn');
      return;
    }
    if (accountLoading || submitting) return;
    submitting = true;
    updatePreview();
    setLoading(btn, true);
    try {
      const res = await api.startRun(Number(accountSelect.value), minutes);
      close();
      if (res?.skipped) {
        toast(res.reason || '今日目标已完成', 'info');
        onDone?.();
        return;
      }
      toast('已开始阅读，进度实时更新中', 'ok');
      onDone?.();
      window.dispatchEvent(new CustomEvent('run:started'));
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      submitting = false;
      if (!closed) {
        setLoading(btn, false);
        updatePreview();
      }
    }
  });
}

export function qrLoginDialog({ onDone } = {}) {
  let flowId = null;
  let startedAt = 0;
  let stage = 'pending';
  let done = false;
  let polling = false;
  // 流程代次：弹窗打开为 0，每次 start()（含「刷新二维码」）自增；
  // 所有迟到响应（qrStart 返回、qrPoll 应用、qrOtp 后的 check）都要求
  // 代次仍为当前才允许改 DOM 或启计时器。
  let gen = 0;
  // 计时器中心登记：intervalId -> 所属代次；所有退出路径（关闭、旋返、
  // 被新流程替换）统一清理，迟到响应因代次守卫也无法再新建 interval。
  const timers = new Map();
  const stopTimers = (whichGen) => {
    for (const [id, idGen] of Array.from(timers)) {
      if (idGen === whichGen) {
        clearInterval(id);
        timers.delete(id);
      }
    }
  };
  const stopAllTimers = () => {
    for (const id of timers.keys()) clearInterval(id);
    timers.clear();
  };
  const registerTimer = (whichGen, id) => {
    timers.set(id, whichGen);
    return id;
  };

  const { mask, close } = openModal({
    title: '扫码登录微信读书',
    subtitle: '打开手机微信「扫一扫」，建议在 1 分钟内扫码；若手机显示 4 位验证码，请在下方输入。',
    body: `
      <div class="qr-box">
        <div class="qr-frame" data-frame>
          <img data-qr alt="微信读书登录二维码">
          <div class="qr-overlay" data-overlay>
            <div>${icon('qr', 'icon')}</div>
            <div data-overlay-text>二维码已失效</div>
          </div>
        </div>
        <div class="qr-countdown" data-countdown>正在获取二维码…</div>
        <div class="qr-status" data-status><span class="pulse-dot"></span>正在获取二维码…</div>
        <div class="otp-wrap" data-otp style="width:100%;max-width:280px">
          <div class="hint" style="margin-bottom:8px">如果手机显示 4 位验证码，请在此输入；显示「登录成功」则无需填写。</div>
          <input class="input otp-input" data-otp-input maxlength="4" inputmode="numeric" placeholder="0000" aria-label="4 位数字验证码">
          <button class="btn btn-primary" data-otp-submit style="width:100%;margin-top:8px">提交验证码</button>
        </div>
        <div class="hint" style="text-align:center">如果手机显示「登录成功」，面板通常会在几秒内自动完成绑定；<br>超过 10 秒仍无反应，请刷新二维码重试。登录凭据会加密保存在本机。</div>
      </div>`,
    footer: `<button class="btn btn-ghost" data-refresh>${icon('refresh')}刷新二维码</button>
      <button class="btn" data-close>关闭</button>`,
    onClose: () => {
      stopAllTimers(); // 关闭即清空全部代次的计时器，不依赖响应路径回收
      done = true;
    },
  });

  const frame = $('[data-frame]', mask);
  const img = $('[data-qr]', mask);
  const statusEl = $('[data-status]', mask);
  const countdownEl = $('[data-countdown]', mask);
  const overlayText = $('[data-overlay-text]', mask);
  const otpBox = $('[data-otp]', mask);
  const otpInput = $('[data-otp-input]', mask);

  const setStatus = (html, error = false) => {
    statusEl.innerHTML = error ? `${icon('alert')}<span>${escapeHtml(html)}</span>` : `<span class="pulse-dot"></span><span>${escapeHtml(html)}</span>`;
    statusEl.style.color = error ? 'var(--red)' : '';
  };

  // 迟到响应统一守卫：关闭（done）、弹窗已移除（isConnected）、代次已推进
  // （新 start/refresh）任一成立，旧流程响应一律丢弃。
  const flowAlive = (myGen) => !done && mask.isConnected && gen === myGen;

  function updateCountdown(myGen) {
    if (!flowAlive(myGen)) return;
    if (stage === 'need_otp') {
      countdownEl.textContent = '已扫码，请按手机提示操作';
      countdownEl.className = 'qr-countdown';
      return;
    }
    const elapsed = Math.max(0, Math.floor(Date.now() / 1000 - startedAt));
    if (elapsed < 5) {
      countdownEl.textContent = '二维码已生成，等待扫码…';
      countdownEl.className = 'qr-countdown';
      return;
    }
    countdownEl.textContent = `已等待 ${elapsed} 秒`;
    countdownEl.className = elapsed >= 120 ? 'qr-countdown urgent' : 'qr-countdown';
  }

  async function start() {
    const myGen = ++gen; // 每次打开/刷新都是新代次
    stopAllTimers(); // 先清掉上一代残留的 interval
    done = false;
    polling = false; // 新代次不再受旧代次在途轮询锁影响
    flowId = null;
    stage = 'pending';
    otpInput.value = '';
    otpBox.style.opacity = '1';
    frame.classList.remove('expired');
    img.removeAttribute('src');
    countdownEl.textContent = '正在获取二维码…';
    countdownEl.className = 'qr-countdown';
    setStatus('正在获取二维码…');
    try {
      const res = await api.qrStart();
      if (!flowAlive(myGen)) return; // 已关闭/被新流程替换：丢弃响应，不建任何计时器
      flowId = res.id;
      img.src = res.qr;
      startedAt = Date.now() / 1000;
      updateCountdown(myGen);
      setStatus('等待微信扫码…');
      // 计时器挂在当前代次名下统一登记；恢复路径全部经 stopTimers/stopAllTimers 清理
      registerTimer(myGen, setInterval(() => updateCountdown(myGen), 1000));
      registerTimer(myGen, setInterval(() => check(myGen), 1200));
    } catch (e) {
      if (!flowAlive(myGen)) return;
      setStatus(e.message, true);
      countdownEl.textContent = '';
    }
  }

  async function check(myGen) {
    if (!flowId || !flowAlive(myGen) || polling) return; // 响应未返回前不重叠发起下一次轮询，避免乱序回写状态
    polling = true;
    try {
      const res = await api.qrPoll(flowId);
      if (!flowAlive(myGen)) return; // 迟到/被替换响应：不改 DOM、不启新计时器
      switch (res.status) {
        case 'pending':
          setStatus('等待微信扫码…');
          break;
        case 'need_otp':
          stage = 'need_otp';
          otpBox.style.opacity = '1';
          updateCountdown(myGen);
          setStatus('已扫码，请输入手机上的 4 位验证码');
          otpInput.focus();
          break;
        case 'binding':
          setStatus('已扫码，正在绑定账号…');
          break;
        case 'confirmed': {
          stopTimers(myGen);
          done = true;
          frame.classList.add('expired');
          overlayText.textContent = '登录成功';
          countdownEl.textContent = '登录成功';
          countdownEl.className = 'qr-countdown';
          const acc = res.account;
          setStatus('登录成功，正在刷新账号…');
          await refreshAccounts();
          toast(acc ? `已绑定「${acc.name}」` : '账号绑定成功', 'ok');
          close();
          onDone?.();
          break;
        }
        case 'expired':
          stopTimers(myGen);
          done = true;
          frame.classList.add('expired');
          overlayText.textContent = '二维码已失效';
          countdownEl.textContent = '二维码已失效';
          countdownEl.className = 'qr-countdown dead';
          setStatus('二维码已失效，请刷新重试', true);
          break;
        case 'failed':
          stopTimers(myGen);
          done = true;
          setStatus(res.error || '登录失败，请重试', true);
          break;
        default:
          break;
      }
    } catch {
      // transient network errors: keep polling
    } finally {
      polling = false;
    }
  }

  $('[data-refresh]', mask).addEventListener('click', start);
  $('[data-close]', mask).addEventListener('click', close);
  $('[data-otp-submit]', mask).addEventListener('click', async () => {
    const myGen = gen; // 提交时锁定代次，等待响应期间被刷新/关闭即丢弃
    const code = $('[data-otp-input]', mask).value.trim();
    if (!/^\d{4}$/.test(code)) {
      toast('请输入 4 位数字验证码', 'warn');
      return;
    }
    const btn = $('[data-otp-submit]', mask);
    setLoading(btn, true);
    try {
      await api.qrOtp(flowId, code);
      if (!flowAlive(myGen)) return; // 迟到响应：不改状态、不触发 check
      toast('验证成功，正在登录…', 'ok');
      stage = 'binding';
      otpInput.value = '';
      setStatus('验证成功，正在绑定账号…');
      check(myGen);
    } catch (e) {
      if (flowAlive(myGen)) toast(e.message, 'error');
    } finally {
      setLoading(btn, false);
    }
  });

  start();
}

export function importAccountDialog({ onDone } = {}) {
  const { mask, close } = openModal({
    title: '导入账号',
    subtitle: '从浏览器复制一次阅读请求，或直接粘贴 Cookie',
    wide: true,
    body: `
      <div class="tabs segmented" data-tabs>
        <button class="tab active" data-tab="curl">粘贴 curl</button>
        <button class="tab" data-tab="cookie">粘贴 Cookie</button>
      </div>
      <div data-panel="curl">
        <div class="field">
          <label class="label" for="imp-curl">curl 命令</label>
          <textarea class="textarea" id="imp-curl" data-curl placeholder="curl 'https://weread.qq.com/web/book/read' -H 'cookie: ...' --data-raw '{...}'"></textarea>
          <div class="hint">在 weread.qq.com 打开任意书籍 → F12 Network → 找到 <code>read</code> 请求 → 右键 Copy as cURL (bash) → 粘贴到这里。会同时解析 Cookie 与请求体模板。</div>
        </div>
      </div>
      <div data-panel="cookie" hidden>
        <div class="field">
          <label class="label" for="imp-cookie">Cookie</label>
          <textarea class="textarea" id="imp-cookie" data-cookie placeholder="wr_vid=...; wr_skey=..."></textarea>
          <div class="hint">从浏览器开发者工具 Application → Cookies 中复制，至少需要 <code>wr_vid</code> 与 <code>wr_skey</code>。</div>
        </div>
      </div>
      <div class="grid-2">
        <div class="field">
          <label class="label" for="imp-name">账号名称 <span class="opt">可选</span></label>
          <input class="input" id="imp-name" data-name placeholder="例如：我的微信读书">
        </div>
        <div class="field">
          <label class="label">高级</label>
          <label class="check" style="height:40px"><input type="checkbox" data-force> 跳过在线校验，强制导入</label>
        </div>
      </div>
      <div class="note">${icon('shield')}<div>导入的 Cookie 会使用本机密钥加密后写入 SQLite。若勾选强制导入，账号状态会标记为「未验证」。</div></div>`,
    footer: `<button class="btn btn-ghost" data-cancel>取消</button>
      <button class="btn btn-primary" data-submit>${icon('check')}导入账号</button>`,
  });

  let tab = 'curl';
  $$('[data-tab]', mask).forEach((btn) => btn.addEventListener('click', () => {
    tab = btn.dataset.tab;
    $$('[data-tab]', mask).forEach((b) => b.classList.toggle('active', b === btn));
    $$('[data-panel]', mask).forEach((p) => { p.hidden = p.dataset.panel !== tab; });
  }));
  $('[data-cancel]', mask).addEventListener('click', close);
  const submit = $('[data-submit]', mask);
  submit.addEventListener('click', async () => {
    const payload = {
      name: $('[data-name]', mask).value.trim(),
      force: $('[data-force]', mask).checked,
    };
    if (tab === 'curl') payload.curl = $('[data-curl]', mask).value;
    else payload.cookie = $('[data-cookie]', mask).value;
    if (!payload.curl && !payload.cookie) {
      toast('请先粘贴内容', 'warn');
      return;
    }
    setLoading(submit, true);
    try {
      const res = await api.importAccount(payload);
      await refreshAccounts();
      close();
      toast(res.warning || '账号导入成功', res.warning ? 'warn' : 'ok');
      onDone?.();
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      setLoading(submit, false);
    }
  });
}

export function editAccountDialog(acc, { onDone } = {}) {
  // 指定书籍直接从该账号的书池里选；不再联网搜索（书池由「书池 · 书籍导入」维护）。
  let book = { id: acc.book_id || '', title: acc.book_title || '' };
  let poolBooks = null; // 该账号视角的启用书籍，加载后缓存

  const { mask, close } = openModal({
    title: `编辑「${acc.name}」`,
    subtitle: `ID #${escapeHtml(acc.seq)} · 这些设置只影响该账号`,
    wide: true,
    body: `
      <div class="grid-2">
        <div class="field">
          <label class="label" for="acc-name">账号名称</label>
          <input class="input" id="acc-name" data-name value="${escapeHtml(acc.name)}">
        </div>
        <div class="field">
          <label class="label" for="acc-minutes">默认阅读时长 <span class="opt">分钟</span></label>
          <input class="input" id="acc-minutes" type="number" min="1" max="1440" data-minutes value="${escapeHtml(acc.default_minutes)}">
        </div>
      </div>
      <div class="field">
        <label class="label" for="acc-daily-goal">每日阅读目标 <span class="opt">分钟</span></label>
        <input class="input" id="acc-daily-goal" type="number" min="0" max="1440" step="1" data-daily-goal value="${escapeHtml(acc.daily_goal_minutes ?? acc.default_minutes)}">
        <div class="hint">填 0 关闭。按站点时区累计本工具的今日阅读记录；保存后对新任务生效，最多补足今日余量。</div>
      </div>
      <div class="grid-2">
        <div class="field">
          <label class="label" for="acc-tick">心跳间隔 <span class="opt">秒</span></label>
          <select class="select" id="acc-tick" data-tick>
            ${[30, 20, 15, 10, 60].map((v) => `<option value="${v}" ${acc.tick_seconds === v ? 'selected' : ''}>${v} 秒</option>`).join('')}
          </select>
          <div class="hint">建议保持 30 秒，过短可能被服务端判定异常。</div>
        </div>
        <div class="field">
          <label class="label" for="acc-mode">阅读模式</label>
          <select class="select" id="acc-mode" data-mode>
            <option value="sequence" ${!acc.mode || acc.mode === 'sequence' ? 'selected' : ''}>顺序书单（推荐）</option>
            <option value="rotate" ${acc.mode === 'rotate' ? 'selected' : ''}>随机轮换书籍</option>
            <option value="custom" ${acc.mode === 'custom' ? 'selected' : ''}>指定书籍 + 进度推进</option>
          </select>
          <div class="hint">推荐顺序书单：按书池顺序逐本读完自动循环。</div>
          <div class="hint">顺序书单按书池顺序逐章推进，随机轮换按池抽取；模板只提供指纹基底。</div>
        </div>
      </div>
      <div class="grid-2">
        <div class="field">
          <label class="label" for="acc-retries">失败自动重试</label>
          <select class="select" id="acc-retries" data-retries>
            ${[0, 1, 2, 3, 5, 8].map((v) => `<option value="${v}" ${Number(acc.max_retries) === v ? 'selected' : ''}>${v === 0 ? '不重试' : `${v} 次`}</option>`).join('')}
          </select>
          <div class="hint">阅读失败后自动重新开始本轮阅读；登录已失效的情况不会重试。</div>
        </div>
        <div class="field">
          <label class="label" for="acc-retry-delay">重试间隔 <span class="opt">秒</span></label>
          <select class="select" id="acc-retry-delay" data-retry-delay>
            ${[5, 10, 15, 30, 60, 120].map((v) => `<option value="${v}" ${Number(acc.retry_delay || 15) === v ? 'selected' : ''}>${v} 秒</option>`).join('')}
          </select>
          <div class="hint">两次重试之间的等待时间。</div>
        </div>
      </div>

      <div class="section-title">阅读内容</div>
      <div data-mode-extra></div>

    `,
    footer: `<button class="btn btn-danger" data-delete>${icon('trash')}删除账号</button>
      <span style="flex:1"></span>
      <button class="btn btn-ghost" data-cancel>取消</button>
      <button class="btn btn-primary" data-save>${icon('check')}保存</button>`,
  });

  const modeSel = $('[data-mode]', mask);
  const modeExtra = $('[data-mode-extra]', mask);

  const renderModeExtra = () => {
    const mode = modeSel.value;
    const MODE_HINTS = {
      custom: '模板只提供设备指纹与会话标识；每次读的书由上面所选书籍决定。',
      rotate: '模板只提供设备指纹与会话标识；书/章节每次心跳从书池随机抽取。',
      sequence: '模板只提供设备指纹与会话标识；书/章节按书池顺序逐个推进。',
    };
    // 先快照：切换模式或书池异步响应触发重绘时，用户已粘贴但未保存
    // 的模板草稿要原样回填（generatePayload 路径会先显式清空，行为不变）。
    const prevBox = $('[data-payload]', modeExtra);
    const draft = prevBox ? prevBox.value : '';
    const wasFocused = prevBox !== null && prevBox === document.activeElement;
    // 请求模板 = 账号的设备指纹底盘，所有模式都会使用（b/c 按模式覆盖）。
    const tplSection = `
      <div class="field">
        <label class="label">请求模板 ${acc.has_payload ? '<span class="opt">已保存</span>' : '<span class="opt">可选 · 所有模式共用</span>'}</label>
        <textarea class="textarea" id="acc-payload" data-payload placeholder="粘贴 read 请求的 curl 或 JSON 请求体" aria-label="请求模板"></textarea>
        <div class="hint">获取方法：浏览器打开 weread.qq.com 阅读任意一本书 → F12 → Network → 找到名为 <code>read</code> 的请求（每 30 秒一条）→ 右键 → 「Copy as cURL (bash)」→ 粘贴到这里。签名 s/sg 自动重算，不用手写。</div>
        <div class="hint">不想抓包？点上面的「生成模板」即可：自动从书池书籍的阅读页拉取指纹并保存；导入书籍时若没有模板也会自动生成。签名 s/sg 自动重算。</div>
        <div class="tpl-actions">
          <button class="btn btn-sm" data-generate-payload title="免抓包：从书池第一本书的阅读页自动生成">${icon('download')}生成模板（免抓包）</button>
          <button class="btn btn-sm" data-save-payload>${icon('check')}保存粘贴的模板</button>
          ${acc.has_payload ? `<button class="btn btn-sm btn-ghost" data-view-payload>查看当前模板</button>` : ''}
          ${acc.has_payload ? `<button class="btn btn-sm btn-ghost" data-clear-payload>清除模板</button>` : ''}
          <span class="hint" data-payload-state>${acc.has_payload ? '当前使用该账号的请求模板' : '未创建请求模板；导入书籍时自动生成，或点上方按钮'}</span>
        </div>
      </div>`;
    modeExtra.innerHTML = `
      ${mode === 'custom' ? customSelectHTML() : ''}
      ${mode !== 'custom' ? `<div class="note" style="margin-bottom:10px">${icon('info')}<div>${mode === 'rotate'
        ? '随机轮换：每次心跳从书池导入的书籍与章节中随机抽取。'
        : '顺序书单：按书池列表的顺序逐本阅读（读完循环）。'}</div></div>` : ''}
      ${tplSection}
    `;
    if (draft) {
      const box = $('[data-payload]', modeExtra);
      if (box) {
        box.value = draft;
        // 简单保焦点：只有重绘前焦点本来就在模板框里才抢回焦点，光标置末尾。
        if (wasFocused) {
          box.focus();
          box.setSelectionRange(box.value.length, box.value.length);
        }
      }
    }
    bindTplWiring();
  };
  const customSelectHTML = () => {
    const list = (poolBooks || []).slice();
    if (book.id && !list.some((b) => b.value === book.id)) {
      list.unshift({ value: book.id, title: book.title });
    }
    return `
      <div class="field">
        <label class="label" for="acc-pool-book">指定书籍 ${poolBooks ? '' : '<span class="opt">书池加载中…</span>'}</label>
        <select class="select" id="acc-pool-book" data-pool-book>
          <option value="">（未指定 · 沿用模板章节）</option>
          ${list.map((b) => `<option value="${escapeHtml(b.value)}" ${b.value === book.id ? 'selected' : ''}>${escapeHtml(b.title || b.value)}</option>`).join('')}
        </select>
        <div class="hint">${poolBooks
          ? '列表来自书池。到「书池 · 书籍导入」可以添加更多可读书籍。'
          : ''}</div>
      </div>`;
  };
  const bindTplWiring = () => {
    $('[data-pool-book]', modeExtra)?.addEventListener('change', (e) => {
      const v = e.target.value;
      const hit = (poolBooks || []).find((b) => b.value === v);
      book = v ? { id: v, title: (hit && hit.title) || book.title || '' } : { id: '', title: '' };
    });
    $('[data-generate-payload]', modeExtra)?.addEventListener('click', async (e) => {
      const btn = e.currentTarget;
      setLoading(btn, true);
      try {
        const res = await api.generatePayload(acc.id);
        acc.has_payload = true;
        const box = $('[data-payload]', modeExtra);
        if (box) box.value = '';
        renderModeExtra();
        toast(`已生成专属模板（${res.book_title || res.book_id}），可开始阅读`, 'ok');
      } catch (err) {
        toast(err.message, 'error');
      } finally {
        setLoading(btn, false);
      }
    });
    $('[data-save-payload]', modeExtra)?.addEventListener('click', async (e) => {
      const btn = e.currentTarget;
      const val = $('[data-payload]', modeExtra).value.trim();
      if (!val) { toast('请粘贴 curl 或请求体', 'warn'); return; }
      setLoading(btn, true);
      try {
        await api.savePayload(acc.id, val.startsWith('{') ? { payload: val } : { curl: val });
        acc.has_payload = true;
        // 重新渲染会更新“已保存”状态，但不要让用户刚粘贴的内容消失。
        renderModeExtra();
        const box = $('[data-payload]', modeExtra);
        if (box) box.value = val;
        toast('模板已保存，该账号所有模式都会用它作为请求基底', 'ok');
      } catch (err) {
        toast(err.message, 'error');
      } finally {
        setLoading(btn, false);
      }
    });
    $('[data-view-payload]', modeExtra)?.addEventListener('click', async (e) => {
      const btn = e.currentTarget;
      const snapBox = $('[data-payload]', modeExtra);
      // 请求前快照：响应若迟到，仅当输入框仍在且用户未再改动（值仍等于快照）才回填；
      // 避免「查看当前模板」覆盖请求期间新粘贴的草稿。
      const snapshot = snapBox ? snapBox.value : '';
      setLoading(btn, true);
      try {
        const res = await api.getPayload(acc.id);
        const box = $('[data-payload]', modeExtra);
        if (box && box.value === snapshot) {
          box.value = JSON.stringify(res.payload || {}, null, 2);
          toast('已把当前模板填入上方输入框（可直接保存覆盖或复制）', 'ok');
        }
      } catch (err) {
        toast(err.message, 'error');
      } finally {
        setLoading(btn, false);
      }
    });
    $('[data-clear-payload]', modeExtra)?.addEventListener('click', async () => {
      const ok = await confirmDialog({
        title: '清除请求模板',
        message: `将删除账号「${acc.name || acc.nickname || ''}」保存的请求模板。之后发起阅读会提示重新生成：可在下一次导入书籍时自动生成，或点「生成模板」，或改用粘贴的抓包模板。`,
        confirmText: '清除',
      });
      if (!ok) return;
      try {
        await api.clearPayload(acc.id);
        acc.has_payload = false;
        renderModeExtra();
        toast('已清除请求模板', 'ok');
      } catch (err) {
        toast(err.message, 'error');
      }
    });
  };
  modeSel.addEventListener('change', renderModeExtra);
  renderModeExtra();

  api.pool(acc.id).then((data) => {
    poolBooks = (data.books || []).filter((b) => b.enabled).map((b) => ({ value: b.value, title: b.title || '' }));
    if (modeSel.value === 'custom') renderModeExtra();
  }).catch(() => { poolBooks = []; });

  $('[data-cancel]', mask).addEventListener('click', close);

  $('[data-save]', mask).addEventListener('click', async (e) => {
    const btn = e.currentTarget;
    // 缺陷 06：字段级校验失败时拒绝提交、就地标注红框/inline 文案并滚动
    // 定位到首个错误字段；原有的 toast 提示与文案保持不变（作辅助说明）。
    clearFieldErrors(mask);
    const nameInput = $('[data-name]', mask);
    const name = nameInput.value.trim();
    if (!name) {
      setFieldError(nameInput, '账号名称不能为空');
      toast('账号名称不能为空', 'warn');
      focusFirstError(mask);
      return;
    }
    const defaultInput = $('[data-minutes]', mask);
    const defaultRaw = defaultInput.value.trim();
    const defaultMinutes = Number(defaultRaw);
    if (defaultRaw === '' || !Number.isInteger(defaultMinutes) || defaultMinutes < 1 || defaultMinutes > 1440) {
      setFieldError(defaultInput, '默认时长需为 1~1440 的整数分钟');
      toast('默认时长需为 1~1440 的整数分钟', 'warn');
      focusFirstError(mask);
      return;
    }
    const goalInput = $('[data-daily-goal]', mask);
    const goalRaw = goalInput.value.trim();
    const dailyGoalMinutes = Number(goalRaw);
    if (goalRaw === '' || !Number.isInteger(dailyGoalMinutes) || dailyGoalMinutes < 0 || dailyGoalMinutes > 1440) {
      setFieldError(goalInput, '每日目标需为 0~1440 的整数分钟');
      toast('每日目标需为 0~1440 的整数分钟', 'warn');
      focusFirstError(mask);
      return;
    }
    const patch = {
      name,
      default_minutes: defaultMinutes,
      daily_goal_minutes: dailyGoalMinutes,
      tick_seconds: Number($('[data-tick]', mask).value) || 30,
      max_retries: Number($('[data-retries]', mask).value) || 0,
      retry_delay: Number($('[data-retry-delay]', mask).value) || 15,
      mode: $('[data-mode]', mask).value,
      book_id: book.id,
      book_title: book.title,
      // 没换书时保留原有的章节指定；换了书则章节随书重置（可在书池里看章节）。
      chapter_id: book.id === acc.book_id ? acc.chapter_id : '',
      chapter_title: book.id === acc.book_id ? acc.chapter_title : '',
    };
    setLoading(btn, true);
    try {
      await api.updateAccount(acc.id, patch);
      await refreshAccounts();
      close();
      toast('设置已保存', 'ok');
      onDone?.();
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      setLoading(btn, false);
    }
  });

  $('[data-delete]', mask).addEventListener('click', async () => {
    const ok = await confirmDialog({
      title: '删除账号',
      message: `确定删除「${acc.name}」吗？该账号的运行记录、日志和定时任务会一并删除，且无法恢复。`,
      confirmText: '确认删除',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteAccount(acc.id);
      await refreshAccounts();
      close();
      toast('账号已删除', 'ok');
      onDone?.();
    } catch (e) {
      toast(e.message, 'error');
    }
  });
}

export function addScheduleDialog({ onDone, schedule = null } = {}) {
  if (!state.accounts.length) {
    toast('请先添加账号', 'warn');
    return;
  }
  const editing = !!schedule;
  const presetCron = schedule?.cron || '0 1 * * *';
  const { mask, close } = openModal({
    title: editing ? '编辑定时任务' : '新建定时任务',
    subtitle: '到点自动开始阅读，无需手动操作',
    body: `
      <div class="field">
        <label class="label" for="sch-account">账号</label>
        ${accountSelectHTML(schedule?.account_id || state.accounts[0].id, { id: 'sch-account' })}
      </div>
      <div class="field">
        <label class="label" for="sch-cron">执行时间</label>
        <div class="chips" data-presets>
          ${[
            ['0 1 * * *', '每天 01:00'], ['0 7 * * *', '每天 07:00'], ['0 12 * * *', '每天 12:00'],
            ['0 23 * * *', '每天 23:00'], ['0 */6 * * *', '每 6 小时'], ['0 */2 * * *', '每 2 小时'],
            ['0 9 * * 1-5', '工作日 09:00'],
          ].map(([c, label]) => `<button type="button" class="chip ${c === presetCron ? 'active' : ''}" data-cron="${c}">${label}</button>`).join('')}
        </div>
        <input class="input mono" id="sch-cron" data-cron-input value="${escapeHtml(presetCron)}" style="margin-top:8px" placeholder="0 1 * * *">
        <div class="hint">标准 5 段 cron：分 时 日 月 周。时区跟随「设置」中的时区。</div>
      </div>
      <div class="grid-2">
        <div class="field">
          <label class="label" for="sch-minutes">阅读时长 <span class="opt">分钟</span></label>
          <input class="input" id="sch-minutes" type="number" min="1" max="1440" data-min value="${escapeHtml(schedule?.minutes || 30)}" placeholder="30">
          <div class="hint">1~1440 的整数分钟；清空后将不会默认 30，保存前需要填写有效值。</div>
        </div>
        <div class="field">
          <label class="label">启用</label>
          <label class="check" style="height:40px"><span class="switch"><input type="checkbox" data-enabled ${schedule?.enabled === false ? '' : 'checked'}><i></i></span> 保存后立即生效</label>
        </div>
      </div>
      <div class="field">
        <label class="label">接下来 3 次</label>
        <div class="list" data-preview><div class="hint">正在计算…</div></div>
      </div>`,
    footer: `<button class="btn btn-ghost" data-cancel>取消</button>
      <button class="btn btn-primary" data-save>${icon('check')}${editing ? '保存' : '创建'}</button>`,
  });

  const cronInput = $('[data-cron-input]', mask);
  const preview = $('[data-preview]', mask);
  let previewSeq = 0;

  const updatePreview = debounce(async () => {
    const cron = cronInput.value.trim();
    if (!cron) return;
    const seq = ++previewSeq;
    try {
      const res = await api.previewSchedule(cron, 3);
      if (seq !== previewSeq) return; // 慢的旧响应不覆盖新结果
      preview.innerHTML = res.times.map((t, i) => `
        <div class="list-row">
          <div class="schedule-icon" style="width:30px;height:30px">${icon('clock')}</div>
          <div class="list-main"><div class="list-title">${escapeHtml(t.text)}</div></div>
          ${i === 0 ? '<span class="badge ok">最近</span>' : ''}
        </div>`).join('');
    } catch (e) {
      if (seq !== previewSeq) return;
      preview.innerHTML = `<div class="hint" style="color:var(--red)">${escapeHtml(e.message)}</div>`;
    }
  }, 450);

  $$('[data-cron]', mask).forEach((btn) => btn.addEventListener('click', () => {
    $$('[data-cron]', mask).forEach((b) => b.classList.remove('active'));
    btn.classList.add('active');
    cronInput.value = btn.dataset.cron;
    updatePreview();
  }));
  cronInput.addEventListener('input', updatePreview);
  updatePreview();

  $('[data-cancel]', mask).addEventListener('click', close);
  const saveBtn = $('[data-save]', mask);
  saveBtn.addEventListener('click', async () => {
    clearFieldErrors(mask);
    const cron = cronInput.value.trim();
    if (!cron) {
      // cron 缺失同样走字段级错误定位（缺陷 06/08 同一机制）。
      setFieldError(cronInput, '请填写 cron 表达式');
      toast('请填写 cron 表达式', 'warn');
      focusFirstError(mask);
      return;
    }
    const minutesInput = $('[data-min]', mask);
    // 缺陷 08：读原始输入校验（新建 / 编辑共用一个入口函数），空串、0、
    // 小数、>1440 都拒绝提交并定位时长字段 —— 不再静默改写为 30。
    const minutes = parseDurationMinutes(minutesInput.value);
    if (minutes === null) {
      setFieldError(minutesInput, '阅读时长需为 1~1440 的整数分钟');
      toast('阅读时长需为 1~1440 的整数分钟（留空、0 与小数均无效）', 'warn');
      focusFirstError(mask);
      return;
    }
    const payload = {
      account_id: Number($('#sch-account', mask).value),
      cron,
      minutes,
      enabled: $('[data-enabled]', mask).checked,
    };
    setLoading(saveBtn, true);
    try {
      if (editing) await api.updateSchedule(schedule.id, payload);
      else await api.createSchedule(payload);
      close();
      toast(editing ? '定时任务已更新' : '定时任务已创建', 'ok');
      onDone?.();
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      setLoading(saveBtn, false);
    }
  });
}

const PUSH_FIELDS = {
  pushplus: [{ key: 'token', label: 'Token', placeholder: 'PushPlus token' }],
  wxpusher: [{ key: 'spt', label: 'SPT', placeholder: 'WxPusher SPT' }],
  telegram: [
    { key: 'bot_token', label: 'Bot Token', placeholder: '123456:ABC...' },
    { key: 'chat_id', label: 'Chat ID', placeholder: '群组或用户 ID' },
    { key: 'proxy', label: '代理地址', placeholder: 'http://127.0.0.1:7890（可选）', optional: true },
  ],
  serverchan: [{ key: 'sendkey', label: 'SendKey', placeholder: 'SCT...' }],
  bark: [
    { key: 'device_key', label: 'Device Key', placeholder: 'Bark 设备 Key' },
    { key: 'server', label: '服务器地址', placeholder: '默认 https://api.day.app（可选）', optional: true },
    { key: 'sound', label: '通知音效', placeholder: '例如 bell（可选）', optional: true },
  ],
  ntfy: [
    { key: 'topic', label: 'Topic', placeholder: '主题名' },
    { key: 'server', label: '服务器地址', placeholder: '默认 https://ntfy.sh（可选）', optional: true },
    { key: 'token', label: '访问令牌', placeholder: '私有主题的 Token（可选）', optional: true },
  ],
  gotify: [
    { key: 'server', label: '服务器地址', placeholder: 'https://gotify.example.com' },
    { key: 'token', label: '应用令牌', placeholder: 'App Token' },
    { key: 'priority', label: '优先级', placeholder: '1-10（可选）', optional: true },
  ],
  pushdeer: [
    { key: 'pushkey', label: 'PushKey', placeholder: 'PushDeer PushKey' },
    { key: 'type', label: '消息类型', placeholder: 'text 或 markdown（默认 text）', optional: true },
  ],
  feishu: [
    { key: 'webhook_url', label: 'Webhook 地址', placeholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/...' },
  ],
  wework: [
    { key: 'webhook_url', label: 'Webhook 地址', placeholder: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...' },
    { key: 'msg_type', label: '消息类型', placeholder: 'text 或 markdown（默认 text）', optional: true },
  ],
  dingtalk: [
    { key: 'webhook_url', label: 'Webhook 地址', placeholder: 'https://oapi.dingtalk.com/robot/send?access_token=...' },
    { key: 'msg_type', label: '消息类型', placeholder: 'text 或 markdown（默认 text）', optional: true },
  ],
  serverchan3: [
    { key: 'uid', label: 'UID', placeholder: 'Server酱³ UID' },
    { key: 'sendkey', label: 'SendKey', placeholder: 'Server酱³ SendKey' },
    { key: 'short', label: '简短描述', placeholder: '可选', optional: true },
    { key: 'tags', label: '标签', placeholder: '多个用 | 分隔（可选）', optional: true },
  ],
  apprise: [
    { key: 'server', label: 'Apprise 服务器', placeholder: 'https://apprise.example.com' },
    { key: 'token', label: '配置 Key', placeholder: '/notify/ 接口的配置 Key' },
    { key: 'urls', label: '目标 URL', placeholder: 'discord://…、mailtos://…，多个用逗号分隔（可选）', optional: true },
  ],
};

// 弹窗里的渠道选择器按键序排列，与 PUSH_FIELDS 一一对应。
const PUSH_TYPES = [
  ['pushplus', 'PushPlus', 'megaphone'],
  ['wxpusher', 'WxPusher', 'chat'],
  ['telegram', 'Telegram', 'plane'],
  ['serverchan', 'ServerChan', 'bell'],
  ['serverchan3', 'Server酱³', 'zap'],
  ['bark', 'Bark', 'moon'],
  ['ntfy', 'Ntfy', 'globe'],
  ['gotify', 'Gotify', 'server'],
  ['pushdeer', 'PushDeer', 'rocket'],
  ['feishu', '飞书', 'chatDuo'],
  ['wework', '企业微信', 'building'],
  ['dingtalk', '钉钉', 'pin'],
  ['apprise', 'Apprise', 'external'],
];

// 每类事件的可自定义模板，字段为 {kind}_title / {kind}_body。
const TEMPLATE_KINDS = [
  ['success', '阅读成功', '微信读书 · 阅读完成', '账号：{{account}}\n计划：{{plan}}\n实际：{{elapsed}}\n状态：{{status}}'],
  ['failed', '阅读失败', '微信读书 · 阅读失败', '账号：{{account}}\n计划：{{plan}}\n实际：{{elapsed}}\n状态：{{status}}\n详情：{{error}}'],
  ['expired', '账号失效', '微信读书 · 账号失效', '账号：{{account}}\n状态：登录已失效，请重新扫码登录\n检查时间：{{time}}'],
];

export function pushChannelDialog({ channel = null, onDone } = {}) {
  const editing = !!channel;
  const type = channel?.type || 'pushplus';
  const { mask, close } = openModal({
    title: editing ? '编辑推送渠道' : '添加推送渠道',
    subtitle: '阅读遇到问题时发送提醒',
    body: `
      <div class="type-grid" data-types>
        ${PUSH_TYPES.map(([t, label, ic]) =>
          `<button type="button" class="type-opt ${t === type ? 'active' : ''}" data-type="${t}"><b>${label}</b>${icon(ic)}</button>`).join('')}
      </div>
      <div class="field">
        <label class="label" for="push-name">渠道名称</label>
        <input class="input" id="push-name" data-name value="${escapeHtml(channel?.name || '')}" placeholder="例如：我的 Telegram">
      </div>
      <div data-fields></div>
      <div class="field">
        <label class="label">启用</label>
        <label class="check"><span class="switch"><input type="checkbox" data-enabled ${channel?.enabled === false ? '' : 'checked'}><i></i></span> 启用该渠道</label>
      </div>
      <div class="section-title" style="text-transform:none;letter-spacing:0">消息模板 <span style="color:var(--muted);font-size:11px;font-weight:500">可选，留空用默认文案</span></div>
      <div class="note" style="margin-bottom:12px">${icon('info')}<div>自动提醒仅用于重试后仍失败或确认登录失效；成功模板保留兼容，正常完成不自动推送。留空使用默认文案。可用变量：{{account}}、{{plan}}、{{elapsed}}、{{status}}、{{error}}、{{success}}、{{failed}}、{{time}}。</div></div>
      ${TEMPLATE_KINDS.map(([k, label, defTitle, defBody]) => `
        <div class="field">
          <label class="label">${label} · 标题</label>
          <input class="input" data-tpl="${k}_title" placeholder="默认：${escapeHtml(defTitle)}"
            value="${escapeHtml(channel?.templates?.[`${k}_title`] || '')}">
          <textarea class="textarea" data-tpl="${k}_body" rows="3" style="margin-top:8px"
            placeholder="默认：${escapeHtml(defBody)}">${escapeHtml(channel?.templates?.[`${k}_body`] || '')}</textarea>
        </div>`).join('')}
      <div class="note">${icon('info')}<div>测试消息会立即发送到对应渠道，请先保存再测试。</div></div>`,
    footer: `<button class="btn btn-ghost" data-cancel>取消</button>
      <button class="btn btn-primary" data-save>${icon('check')}保存</button>`,
  });

  let currentType = type;
  const fieldsEl = $('[data-fields]', mask);

  const renderFields = () => {
    fieldsEl.innerHTML = PUSH_FIELDS[currentType].map((f) => `
      <div class="field">
        <label class="label">${f.label} ${f.optional ? '<span class="opt">可选</span>' : ''}</label>
        <input class="input" data-key="${f.key}" placeholder="${f.placeholder}"
          value="${escapeHtml(channel?.config?.[f.key] || '')}">
      </div>`).join('');
  };
  renderFields();

  $$('[data-type]', mask).forEach((btn) => btn.addEventListener('click', () => {
    $$('[data-type]', mask).forEach((b) => b.classList.remove('active'));
    btn.classList.add('active');
    currentType = btn.dataset.type;
    renderFields();
  }));

  $('[data-cancel]', mask).addEventListener('click', close);
  const saveBtn = $('[data-save]', mask);
  saveBtn.addEventListener('click', async () => {
    const config = {};
    $$('[data-key]', mask).forEach((input) => { config[input.dataset.key] = input.value.trim(); });
    const templates = {};
    $$('[data-tpl]', mask).forEach((el) => { templates[el.dataset.tpl] = el.value.trim(); });
    const payload = {
      name: $('[data-name]', mask).value.trim(),
      type: currentType,
      config,
      templates,
      enabled: $('[data-enabled]', mask).checked,
    };
    setLoading(saveBtn, true);
    try {
      if (editing) await api.updatePush(channel.id, payload);
      else await api.createPush(payload);
      close();
      toast(editing ? '渠道已更新' : '渠道已添加', 'ok');
      onDone?.();
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      setLoading(saveBtn, false);
    }
  });
}

export function runDetailDrawer(runId, { onDone } = {}) {
  const mask = document.createElement('div');
  mask.className = 'drawer-mask';
  const drawer = document.createElement('div');
  drawer.className = 'drawer';
  drawer.setAttribute('role', 'dialog');
  drawer.setAttribute('aria-modal', 'true');
  drawer.setAttribute('aria-label', '运行详情');
  drawer.innerHTML = `
    <div class="drawer-head">
      <div>
        <div class="modal-title" data-title>运行详情</div>
        <div class="modal-sub" data-sub>加载中…</div>
      </div>
      <button class="modal-close" data-close aria-label="关闭">${icon('close')}</button>
    </div>
    <div class="drawer-body">
      <div class="grid-2" data-stats style="margin-bottom:14px"></div>
      <div data-run-error style="margin-bottom:14px"></div>
      <div class="card-title" style="margin-bottom:10px">${icon('list')}实时日志</div>
      <div class="hint" data-log-status style="margin-bottom:8px">正在加载完整日志…</div>
      <div class="console" data-console><div class="empty-log">加载日志…</div></div>
    </div>
    <div class="drawer-foot">
      <button class="btn btn-sm btn-ghost" data-copy disabled>${icon('copy')}日志加载中…</button>
      <button class="btn btn-sm btn-danger" data-stop>${icon('stop')}停止运行</button>
      <button class="btn btn-sm" data-close2>关闭</button>
    </div>`;
  document.body.appendChild(mask);
  document.body.appendChild(drawer);
  lockScroll();

  let closed = false;
  let historyAfter = 0;
  let historyLoading = false;
  let historyComplete = false;
  let catchUpAfterLoad = false;
  let logRequestToken = 0;
  let logController = null;
  let lastRenderedId = 0;
  let initialLogError = '';
  const seenLogIDs = new Set();
  const pendingLiveLogs = [];
  let baseRun = null;
  const consoleEl = $('[data-console]', drawer);
  const logStatus = $('[data-log-status]', drawer);
  const copyBtn = $('[data-copy]', drawer);
  const stopBtn = $('[data-stop]', drawer);

  const onKey = (e) => {
    if (e.key !== 'Escape') return;
    // 上层若还有 modal（如确认框），Escape 交给它处理
    if (document.querySelector('.modal-mask')) return;
    close();
  };

  const close = () => {
    if (closed) return;
    closed = true;
    logRequestToken++;
    logController?.abort();
    mask.remove();
    drawer.remove();
    unlockScroll();
    document.removeEventListener('keydown', onKey);
    window.removeEventListener('app:event', onEvent);
    onDone?.();
  };
  mask.addEventListener('click', close);
  $('[data-close]', drawer).addEventListener('click', close);
  $('[data-close2]', drawer).addEventListener('click', close);
  document.addEventListener('keydown', onKey);
  $('[data-close]', drawer).focus();

  const appendLogs = (logs) => {
    if (!logs?.length) return;
    if (consoleEl.querySelector('.empty-log')) consoleEl.innerHTML = '';
    for (const log of logs) {
      const id = Number(log.id) || 0;
      if (id && seenLogIDs.has(id)) continue;
      if (id) seenLogIDs.add(id);
      const line = document.createElement('div');
      line.className = `log-line ${escapeHtml(log.level)}`;
      if (id) line.dataset.logId = String(id);
      line.innerHTML = `<span class="t">${fmtTime(log.ts, state.tz, true)}</span><span class="lv">${escapeHtml(String(log.level).toUpperCase())}</span><span class="m">${escapeHtml(log.message)}</span>`;
      if (id && id < lastRenderedId) {
        const next = Array.from(consoleEl.children).find((el) => Number(el.dataset.logId) > id);
        consoleEl.insertBefore(line, next || null);
      } else {
        consoleEl.appendChild(line);
      }
      lastRenderedId = Math.max(lastRenderedId, id);
    }
    consoleEl.scrollTop = consoleEl.scrollHeight;
  };

  const loadAllLogs = async () => {
    if (closed || historyLoading) return;
    historyLoading = true;
    historyComplete = false;
    const token = ++logRequestToken;
    const controller = new AbortController();
    logController = controller;
    copyBtn.disabled = true;
    copyBtn.innerHTML = `${icon('copy')}日志加载中…`;
    logStatus.textContent = initialLogError
      ? `${initialLogError}；正在通过分页接口重新加载…`
      : '正在加载完整日志…';
    try {
      let more = true;
      while (more && !closed && token === logRequestToken) {
        const res = await api.runLogs(runId, { afterId: historyAfter, limit: 500, signal: controller.signal });
        if (closed || token !== logRequestToken) return;
        const logs = res?.logs;
        if (!Array.isArray(logs)) throw new Error('日志响应格式错误');
        const lastID = logs.length ? Number(logs[logs.length - 1].id) : historyAfter;
        if (logs.length && (!Number.isFinite(lastID) || lastID <= historyAfter)) {
          throw new Error('日志分页未推进');
        }
        appendLogs(logs);
        historyAfter = lastID;
        more = logs.length === 500 || catchUpAfterLoad;
        catchUpAfterLoad = false;
      }
      if (closed || token !== logRequestToken) return;
      pendingLiveLogs.sort((a, b) => (Number(a.id) || 0) - (Number(b.id) || 0));
      appendLogs(pendingLiveLogs);
      pendingLiveLogs.length = 0;
      const logCount = consoleEl.querySelectorAll('.log-line').length;
      if (!logCount) consoleEl.innerHTML = '<div class="empty-log">暂无日志</div>';
      historyLoading = false;
      historyComplete = true;
      initialLogError = '';
      logStatus.textContent = `已加载当前全部日志（${logCount} 条）`;
      copyBtn.disabled = false;
      copyBtn.innerHTML = `${icon('copy')}复制日志`;
    } catch (e) {
      if (closed || token !== logRequestToken) return;
      historyLoading = false;
      logStatus.innerHTML = `<div class="note warn">${icon('alert')}<div>日志加载中断：${escapeHtml(e.message)} <button class="btn btn-sm" data-retry-logs>重试加载</button></div></div>`;
      $('[data-retry-logs]', logStatus).addEventListener('click', loadAllLogs);
    } finally {
      if (logController === controller) logController = null;
    }
  };

  const paintRun = (run) => {
    $('[data-title]', drawer).textContent = `运行 #${run.id}`;
    $('[data-sub]', drawer).textContent = `${run.account_name || ''} · ${run.trigger === 'schedule' ? '定时任务' : '手动开始'} · ${fmtClock(run.started_at, state.tz)}`;
    const pct = run.plan_seconds ? Math.min(100, Math.round((run.elapsed_seconds / run.plan_seconds) * 100)) : 0;
    $('[data-stats]', drawer).innerHTML = `
      <div class="card" style="padding:14px">
        <div class="stat-label">进度</div>
        <div class="stat-value" style="font-size:22px">${fmtMinutes(run.elapsed_seconds)}<small>/ ${fmtMinutes(run.plan_seconds)} 分</small></div>
        <div class="bar" style="margin-top:10px"><i style="width:${pct}%"></i></div>
      </div>
      <div class="card" style="padding:14px">
        <div class="stat-label">状态</div>
        <div style="margin-top:8px">${runStatusBadge(run.status, '阅读中')}</div>
        <div class="hint" style="margin-top:8px">成功 ${run.success_ticks} 次 · 失败 ${run.failed_ticks} 次</div>
      </div>`;
    // 失败原因固定展示（含服务重启等没有日志的失败场景）。
    $('[data-run-error]', drawer).innerHTML = run.status === 'failed' && run.error
      ? `<div class="note danger">${icon('alert')}<div><b>失败原因：</b>${escapeHtml(run.error)}</div></div>`
      : '';
    stopBtn.style.display = run.status === 'running' ? '' : 'none';
  };

  const onEvent = (e) => {
    const ev = e.detail;
    if (ev.run_id !== runId) return;
    if (ev.type === 'run.log' && ev.data) {
      if (historyComplete) appendLogs([ev.data]);
      else pendingLiveLogs.push(ev.data);
    }
    if (ev.type === 'run.tick' && ev.data) {
      const d = ev.data;
      if (!baseRun) {
        baseRun = {
          id: runId, account_name: d.account_name, trigger: 'manual',
          started_at: d.started_at, elapsed_seconds: 0, plan_seconds: d.target || 0,
          success_ticks: 0, failed_ticks: 0, status: 'running',
        };
      }
      baseRun.elapsed_seconds = d.elapsed;
      baseRun.plan_seconds = d.target;
      baseRun.success_ticks = d.success;
      baseRun.failed_ticks = d.failed;
      baseRun.status = 'running';
      paintRun(baseRun);
    }
    if (ev.type === 'run.end') {
      if (historyLoading) catchUpAfterLoad = true;
      else if (historyComplete) loadAllLogs();
      stopBtn.style.display = 'none';
      if (baseRun) baseRun.status = ev.data?.status || 'success';
      if (baseRun) paintRun(baseRun);
      api.run(runId).then((res) => {
        if (closed || !res?.run) return;
        baseRun = res.run;
        paintRun(res.run);
      }).catch(() => { /* 忽略结账查询失败，界面已有 end 状态 */ });
      toast(`运行 #${runId} 已结束`, 'info');
    }
  };
  window.addEventListener('app:event', onEvent);

  stopBtn.addEventListener('click', async () => {
    const ok = await confirmDialog({ title: '停止运行', message: '确定停止本次阅读吗？已完成的时长会保留。', confirmText: '停止', danger: true });
    if (!ok) return;
    try {
      await api.stopRun(runId);
      toast('已停止', 'ok');
    } catch (e) {
      toast(e.message, 'error');
    }
  });

  copyBtn.addEventListener('click', () => {
    if (!historyComplete) return;
    const text = Array.from(consoleEl.querySelectorAll('.log-line')).map((l) => l.textContent).join('\n');
    copyText(text);
  });

  (async () => {
    try {
      const res = await api.run(runId);
      if (closed) return;
      baseRun = res.run;
      paintRun(res.run);
      initialLogError = res.logs_error || '';
      await loadAllLogs();
    } catch (e) {
      toast(e.message, 'error');
      close();
    }
  })();
}
