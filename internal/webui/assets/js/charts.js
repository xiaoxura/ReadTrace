import { cssVar, fmtMinutes, escapeHtml } from './util.js';

let ringSeq = 0;

// 缺陷 09：tooltip 水平定位 clamp（纯函数，.tmp 模拟测试直接 import 断言）。
// 中心点 cx 的期望左缘 = cx - w/2，压回 [padding, hostWidth - padding - w]
//（容器窄到放不下时贴左缘），返回期望左缘坐标；调用方为悬浮层的
// translate(-50%) 中心定位方式补回 w/2。
export function clampTipLeft(cx, tipWidth, hostWidth, padding = 2) {
  const minLeft = padding;
  const maxLeft = Math.max(minLeft, hostWidth - padding - tipWidth);
  return Math.min(Math.max(cx - tipWidth / 2, minLeft), maxLeft);
}

export function barChart(host, data, opts = {}) {
  if (!host) return;
  host.__chart = { data, opts };
  paintBar(host);

  if (!host.__bound) {
    host.__bound = true;
    host.addEventListener('mouseleave', () => {
      const tip = host.querySelector('.chart-tip');
      if (tip) tip.style.display = 'none';
    });
  }
}

function paintBar(host) {
  const { data, opts } = host.__chart || {};
  if (!Array.isArray(data)) return;
  const format = opts.format || ((v) => `${fmtMinutes(v)} 分钟`);
  const height = opts.height || 196;
  const width = Math.max(300, host.clientWidth || 640);
  const pad = { t: 12, r: 6, b: 24, l: 6 };
  const innerW = width - pad.l - pad.r;
  const innerH = height - pad.t - pad.b;
  const values = data.map((d) => Number(d.value) || 0);
  const max = Math.max(60, ...values) * 1.14;
  const n = data.length || 1;
  const step = innerW / n;
  const barW = Math.max(3, Math.min(26, step * 0.54));
  const radius = Math.min(4, barW / 2);

  // 循环外一次取齐颜色变量，避免循环内反复触发 getComputedStyle
  const colorLine = cssVar('--line');
  const colorMuted = cssVar('--muted');
  const colorSurface3 = cssVar('--surface-3');
  const colorAccent = cssVar('--accent');
  const colorAccent2 = cssVar('--accent-2');

  const gridColor = colorLine;
  let grid = '';
  for (let i = 0; i <= 3; i++) {
    const y = pad.t + (innerH / 3) * i;
    grid += `<line x1="${pad.l}" y1="${y}" x2="${width - pad.r}" y2="${y}" stroke="${gridColor}" stroke-width="1" stroke-dasharray="3 5"/>`;
  }

  let bars = '';
  data.forEach((d, i) => {
    const v = Number(d.value) || 0;
    const h = Math.max(0, (v / max) * innerH);
    const x = pad.l + step * i + (step - barW) / 2;
    const y = pad.t + innerH - h;
    const isLast = i === n - 1;
    const op = isLast ? 1 : 0.55;
    if (h >= 2) {
      bars += `<rect class="chart-bar" data-i="${i}" x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${barW.toFixed(1)}" height="${h.toFixed(1)}" rx="${radius}" fill="url(#barGrad)" opacity="${op}"/>`;
    } else {
      bars += `<rect class="chart-bar" data-i="${i}" x="${x.toFixed(1)}" y="${(pad.t + innerH - 2).toFixed(1)}" width="${barW.toFixed(1)}" height="2" rx="1" fill="${colorSurface3}" opacity="1"/>`;
    }
  });

  const maxLabels = Math.max(3, Math.floor(innerW / 58));
  const labelEvery = Math.max(1, Math.ceil(n / maxLabels));
  let labels = '';
  data.forEach((d, i) => {
    if (i % labelEvery !== 0) return;
    const x = pad.l + step * i + step / 2;
    labels += `<text x="${x.toFixed(1)}" y="${height - 6}" text-anchor="middle" font-size="9.5" fill="${colorMuted}" font-family="var(--font)">${escapeHtml(d.label)}</text>`;
  });

  host.innerHTML = `
    <svg class="chart-svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">
      <defs>
        <linearGradient id="barGrad" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color="${colorAccent2}"/>
          <stop offset="100%" stop-color="${colorAccent}"/>
        </linearGradient>
      </defs>
      ${grid}
      ${bars}
      ${labels}
    </svg>
    <div class="chart-tip"></div>`;

  const tip = host.querySelector('.chart-tip');
  host.querySelectorAll('rect.chart-bar').forEach((rect) => {
    const showTip = () => {
      const i = Number(rect.dataset.i);
      const d = data[i];
      const hostRect = host.getBoundingClientRect();
      const r = rect.getBoundingClientRect();
      tip.innerHTML = `${escapeHtml(d.label)} · <b>${escapeHtml(format(d.value))}</b>`;
      tip.style.display = 'block';
      // 缺陷 09：先按提示实际宽度 clamp 左缘，再换算成 translate(-50%) 的
      // 中心点 left；最右/最左柱的日期、数值与单位都完整落在容器内。
      const cx = r.left - hostRect.left + r.width / 2;
      const tipWidth = tip.offsetWidth;
      const left = clampTipLeft(cx, tipWidth, host.clientWidth);
      tip.style.left = `${left + tipWidth / 2}px`;
      tip.style.top = `${r.top - hostRect.top + 2}px`;
    };
    rect.addEventListener('mouseenter', showTip);
    rect.addEventListener('click', showTip);
    rect.addEventListener('mouseleave', () => {
      tip.style.display = 'none';
    });
  });
}

export function ringSVG(percent, size = 132, stroke = 9) {
  const p = Math.max(0, Math.min(1, Number(percent) || 0));
  const r = (size - stroke) / 2;
  const c = 2 * Math.PI * r;
  const off = c * (1 - p);
  const id = `rg${++ringSeq}`;
  return `<svg width="${size}" height="${size}" viewBox="0 0 ${size} ${size}" aria-hidden="true">
    <defs>
      <linearGradient id="${id}" x1="0" y1="0" x2="1" y2="1">
        <stop offset="0%" stop-color="${cssVar('--accent-2')}"/>
        <stop offset="100%" stop-color="${cssVar('--accent')}"/>
      </linearGradient>
    </defs>
    <circle cx="${size / 2}" cy="${size / 2}" r="${r}" fill="none" stroke="${cssVar('--surface-3')}" stroke-width="${stroke}"/>
    <circle class="ring-progress" cx="${size / 2}" cy="${size / 2}" r="${r}" fill="none" stroke="url(#${id})"
      stroke-width="${stroke}" stroke-linecap="round" stroke-dasharray="${c.toFixed(2)}"
      stroke-dashoffset="${off.toFixed(2)}" transform="rotate(-90 ${size / 2} ${size / 2})"/>
  </svg>`;
}

export function sparkline(values, width = 96, height = 30) {
  if (!values.length) return '';
  const max = Math.max(...values, 1);
  const min = Math.min(...values, 0);
  const span = max - min || 1;
  const step = width / Math.max(1, values.length - 1);
  const points = values.map((v, i) => {
    const x = i * step;
    const y = height - 3 - ((v - min) / span) * (height - 6);
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  });
  const line = points.join(' ');
  const area = `0,${height} ${line} ${width},${height}`;
  const id = `sp${++ringSeq}`;
  return `<svg width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" aria-hidden="true">
    <defs><linearGradient id="${id}" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0%" stop-color="${cssVar('--accent')}" stop-opacity=".28"/>
      <stop offset="100%" stop-color="${cssVar('--accent')}" stop-opacity="0"/>
    </linearGradient></defs>
    <polygon points="${area}" fill="url(#${id})"/>
    <polyline points="${line}" fill="none" stroke="${cssVar('--accent')}" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>
  </svg>`;
}

export function redrawCharts() {
  document.querySelectorAll('.chart-host').forEach((host) => {
    if (host.__chart) paintBar(host);
  });
}
