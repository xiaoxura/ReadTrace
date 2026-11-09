import { api } from '../api.js';
import { state, refreshAccounts } from '../state.js';
import { icon } from '../icons.js';
import { runDetailDrawer, startRunDialog } from '../components.js';
import { renderError, skeletonHTML, runRowHTML, bindRunRows, bindStopButtons, bindRunStateRefresh } from '../helpers.js';
import { $, $$, escapeHtml, fmtMinutes, pageHeaderHTML, toast, setLoading } from '../util.js';

// 服务端分页（缺陷 18）：每页 300 条由服务端按 offset 切出（GET /api/runs
// 的 limit/offset 直接落到 ListRuns 的 LIMIT/OFFSET）。请求时多拉一条，
// 用「第 301 条是否存在」判断有无下一页；接收列表里仍最多展示 300 行。
const SERVER_PAGE = 300;

// 缺陷 03：账号筛选变更要写入 sessionStorage 的值（纯函数，.tmp 模拟测试
// 直接 import 断言）。仅在列出的账号中有效才接受，否则回 0（全部账号）。
export function nextAccountFilter(nextId, accounts) {
  const id = Number(nextId) || 0;
  return String(accounts.some((a) => a.id === id) ? id : 0);
}

export async function renderRuns(ctx) {
  const { root } = ctx;
  root.innerHTML = skeletonHTML(['height:70px;width:45%', 'height:300px']);

  if (!state.accounts.length) {
    try { await refreshAccounts(); } catch { /* ignore */ }
  }
  if (!ctx.isActive()) return;
  const saved = Number(sessionStorage.getItem('wxread-runs-account') || 0);
  const accountId = state.accounts.some((a) => a.id === saved) ? saved : 0;

  // 服务端页游标：offset = (页码-1) × 每页条数，跨刷新保留在 sessionStorage。
  let offset = Number(sessionStorage.getItem('wxread-runs-offset') || 0);
  if (offset < 0 || offset % SERVER_PAGE !== 0) offset = 0;
  let page = 1; // 实际页码在拉取成功后按 offset 推进
  let hasMore = false;

  // fetchPage：请求 SERVER_PAGE+1 条，在未截断的原始响应上判定 hasMore
  //（第 301 条存在 → 还有下一页），再截出最多 300 条用于展示。
  const fetchPage = async (off) => {
    const raw = await api.runs(accountId, SERVER_PAGE + 1, off);
    const runs = raw.runs || [];
    return {
      runs: runs.slice(0, SERVER_PAGE),
      active: raw.active || [],
      hasMore: runs.length > SERVER_PAGE,
    };
  };

  let data;
  try {
    data = await fetchPage(offset);
    // 游标悬空防护：偏移超出可用记录（记录被清理/保留期满）时回到第 1 页。
    if (offset > 0 && !(data.runs || []).length) {
      offset = 0;
      data = await fetchPage(0);
      sessionStorage.removeItem('wxread-runs-offset');
    }
  } catch (e) {
    renderError(root, e, { onRetry: () => ctx.reload() });
    return;
  }
  if (!ctx.isActive()) return;
  hasMore = data.hasMore === true;
  page = offset / SERVER_PAGE + 1;

  root.innerHTML = `
    ${pageHeaderHTML({
      title: '阅读记录',
      sub: `第 ${page} 页（最近起每页 ${SERVER_PAGE} 条）`,
      actions: `
        <button class="btn btn-ghost" id="btn-refresh">${icon('refresh')}<span>刷新</span></button>
        <button class="btn btn-primary" id="btn-start">${icon('play')}<span>开始阅读</span></button>`,
    })}

    <div class="tool-row">
      <select class="select" id="run-filter" style="width:auto;min-width:150px" aria-label="按账号筛选">
        <option value="0" ${!accountId ? 'selected' : ''}>全部账号</option>
        ${state.accounts.map((a) => `<option value="${a.id}" ${a.id === accountId ? 'selected' : ''}>${escapeHtml(a.name)}</option>`).join('')}
      </select>
      <select class="select" id="run-status" style="width:auto;min-width:120px" aria-label="按状态筛选">
        <option value="all">全部状态</option>
        <option value="running">进行中</option>
        <option value="success">成功</option>
        <option value="failed">失败</option>
        <option value="stopped">已停止</option>
      </select>
      <select class="select" id="run-trigger" style="width:auto;min-width:120px" aria-label="按触发方式筛选">
        <option value="all">全部触发</option>
        <option value="manual">手动</option>
        <option value="schedule">定时</option>
      </select>
      <span class="hint">筛选仅作用于当前加载的这一页（每页 ${SERVER_PAGE} 条，最近优先）；上一页 / 下一页按服务端 offset 翻页。</span>
    </div>

    ${(data.active || []).length ? `<div class="card" style="margin-bottom:16px">
      <div class="card-head"><div><div class="card-title">${icon('wifi')}正在运行</div><div class="card-sub">点击查看实时日志</div></div></div>
      <div class="list">
        ${data.active.map((p) => {
          const pct = p.target ? Math.min(100, Math.round((p.elapsed / p.target) * 100)) : 0;
          return `<div class="list-row">
            <div class="schedule-icon on">${icon('play')}</div>
            <div class="list-main">
              <div class="list-title">${escapeHtml(p.account_name)} <span class="badge run"><span class="pulse-dot"></span>${pct}%</span></div>
              <div class="list-sub">已读 ${fmtMinutes(p.elapsed)} / ${fmtMinutes(p.target)} 分钟 · 成功 ${escapeHtml(p.success)} 次</div>
            </div>
            <button class="btn btn-sm" data-open="${p.run_id}">查看</button>
            <button class="btn btn-sm btn-danger" data-stop="${p.run_id}">停止</button>
          </div>`;
        }).join('')}
      </div>
    </div>` : ''}

    <article class="card">
      <div class="card-head">
        <div><div class="card-title">${icon('list')}全部记录</div><div class="card-sub">点击任意一行查看日志 · 服务端分页，可翻到保留期内的更早记录</div></div>
      </div>
      <div data-runs-table></div>
    </article>
  `;
  if (!ctx.isActive()) return;

  $('#run-filter', root)?.addEventListener('change', (e) => {
    // 缺陷 03：change 时先把所选账号持久化（重载从该值恢复下拉与查询，
    // 旧实现只清游标不保存，重绘即回退到旧账号），再把分页游标归零
    //（各账号的记录序列不同，沿用旧 offset 没有意义），最后重载拉取。
    sessionStorage.setItem('wxread-runs-account', nextAccountFilter(e.target.value, state.accounts));
    sessionStorage.removeItem('wxread-runs-offset');
    ctx.reload();
  });
  $('#btn-refresh', root)?.addEventListener('click', () => ctx.reload());
  $('#btn-start', root)?.addEventListener('click', () => {
    startRunDialog({ accountId, onDone: () => ctx.reload() });
  });

  const statusSel = $('#run-status', root);
  const triggerSel = $('#run-trigger', root);
  const statusFilter = sessionStorage.getItem('wxread-runs-status') || 'all';
  const triggerFilter = sessionStorage.getItem('wxread-runs-trigger') || 'all';
  if (statusSel) statusSel.value = [...statusSel.options].some((o) => o.value === statusFilter) ? statusFilter : 'all';
  if (triggerSel) triggerSel.value = [...triggerSel.options].some((o) => o.value === triggerFilter) ? triggerFilter : 'all';

  const tableHost = $('[data-runs-table]', root);

  // drawTable 只做客户端筛选 + 表格渲染：数据来自服务端切好的一页。
  const drawTable = () => {
    const runs = data.runs || [];
    const status = statusSel?.value || 'all';
    const trigger = triggerSel?.value || 'all';
    const filtered = runs.filter((r) =>
      (status === 'all' || r.status === status) && (trigger === 'all' || r.trigger === trigger));
    const total = filtered.length;

    if (!total) {
      const pageEmpty = runs.length > 0; // 页里有数据但筛完了 → 筛选空态
      tableHost.innerHTML = `<div class="empty" style="padding:44px 10px">
        <div class="empty-ico">${icon('list')}</div>
        <h3>${pageEmpty ? '本页筛选后没有匹配的记录' : '本页还没有记录'}</h3>
        <p>${pageEmpty
          ? '筛选只作用于当前这一页；试试切回「全部」，或用上一页 / 下一页翻看其他页。'
          : '点击右上角「开始阅读」，或创建一个定时任务。'}</p>
      </div>
      ${pageEmpty ? pagerHTML(0) : ''}
      `;
      // 筛选空态也要能翻页：留出上一页/下一页出路。
      if (pageEmpty) bindServerPager(tableHost);
      return;
    }
    tableHost.innerHTML = `
      <div class="table-wrap"><table class="table runs-table table-wide">
        <thead><tr><th>状态</th><th>账号</th><th>触发</th><th>时长</th><th>进度</th><th>开始时间</th><th>结束时间</th><th></th></tr></thead>
        <tbody data-run-rows>${filtered.map((r) => runRowHTML(r, { endTime: true })).join('')}</tbody>
      </table></div>
      ${pagerHTML(total)}
    `;
    bindServerPager(tableHost);
    bindRunRows(tableHost, (id) => runDetailDrawer(id, { onDone: () => ctx.reload() }));
    bindStopButtons(tableHost, { onDone: () => ctx.reload() });
  };

  // pagerHTML：服务端分页没有总量概念，page/hint 与上下页按钮按游标生成。
  const pagerHTML = (total) => `
    <div class="table-pager" data-pager>
      <span class="hint">本页筛选后 ${total} 条 · 第 ${page} 页</span>
      <span class="table-pager-gap"></span>
      <span class="hint">每页 ${SERVER_PAGE} 条（最近起）</span>
      <button class="btn btn-sm btn-ghost" data-pager-prev ${page <= 1 ? 'disabled' : ''}>上一页</button>
      <button class="btn btn-sm btn-ghost" data-pager-next ${!hasMore ? 'disabled' : ''}>下一页</button>
    </div>
  `;

  const updatePagerEnabledState = () => {
    const prev = $('[data-pager-prev]', tableHost);
    const next = $('[data-pager-next]', tableHost);
    if (prev) prev.disabled = page <= 1;
    if (next) next.disabled = !hasMore;
  };

  // goPage(delta)：点击后先拉目标页，成功才替换当前数据并更新游标；
  // 拉取失败时保持原页，仅通过 toast 提示（缺陷 18 的交互要求）。
  const goPage = async (delta) => {
    const prevBtn = $('[data-pager-prev]', tableHost);
    const nextBtn = $('[data-pager-next]', tableHost);
    const busyTarget = delta < 0 ? prevBtn : nextBtn;
    setLoading(busyTarget, true);
    if (prevBtn && busyTarget !== prevBtn) prevBtn.disabled = true;
    if (nextBtn && busyTarget !== nextBtn) nextBtn.disabled = true;
    const targetOffset = offset + delta * SERVER_PAGE;
    try {
      const next = await fetchPage(targetOffset);
      if (!ctx.isActive()) return; // 迟到响应不得覆盖已切换/重绘的新页面
      const cameUpEmpty = delta > 0 && !next.runs.length;
      if (cameUpEmpty) {
        hasMore = false;
        updatePagerEnabledState();
        toast('已到最后一页', 'warn');
        return;
      }
      data = next;
      offset = targetOffset;
      hasMore = next.hasMore === true;
      page = offset / SERVER_PAGE + 1;
      sessionStorage.setItem('wxread-runs-offset', String(offset));
      // 页头与表格一同刷新，保证页码显示与数据一致。
      const sub = $('.page-sub', root) || $('#page-sub', root);
      if (sub) sub.textContent = `第 ${page} 页（最近起每页 ${SERVER_PAGE} 条）`;
      drawTable();
    } catch (err) {
      // 失败保持原页：游标与数据都不动，只提示。
      toast(`加载第 ${offset / SERVER_PAGE + 1 + delta} 页失败：${err.message}`, 'error');
      updatePagerEnabledState();
    } finally {
      setLoading(busyTarget, false);
      if (prevBtn && busyTarget !== prevBtn) prevBtn.disabled = page <= 1;
      if (nextBtn && busyTarget !== nextBtn) nextBtn.disabled = !hasMore;
    }
  };

  const bindServerPager = (host) => {
    $('[data-pager]', host)?.addEventListener('click', (e) => {
      const btn = e.target.closest('button');
      if (!btn || btn.disabled) return;
      if (btn.dataset.pagerPrev !== undefined) goPage(-1);
      else if (btn.dataset.pagerNext !== undefined) goPage(1);
    });
  };

  statusSel?.addEventListener('change', () => {
    sessionStorage.setItem('wxread-runs-status', statusSel.value);
    drawTable(); // 筛选只重绘当前页，不重新拉取
  });
  triggerSel?.addEventListener('change', () => {
    sessionStorage.setItem('wxread-runs-trigger', triggerSel.value);
    drawTable();
  });

  drawTable();

  $$('[data-open]', root).forEach((btn) => btn.addEventListener('click', () => {
    runDetailDrawer(Number(btn.dataset.open), { onDone: () => ctx.reload() });
  }));

  ctx.setHandler(bindRunStateRefresh(ctx));
}
