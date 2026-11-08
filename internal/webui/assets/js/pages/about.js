import { icon } from '../icons.js';
import { pageHeaderHTML } from '../util.js';

const REPO_URL = 'https://github.com/xiaoxura/ReadTrace';

// “关于”是纯静态页：不请求任何接口，只展示项目自描述信息。
// 版本、运行时长等运行时数据属于「设置 → 运行信息」，不要加回到这里。
const STACK = [
  ['zap', 'Go 单二进制', '后端与前端打包进一个可执行文件，无 Node/Python 运行时，编译即部署'],
  ['server', 'SQLite 单文件存储', '账号、运行记录、定时任务与统计全部本地落库，复制 data/ 目录即可迁移'],
  ['wifi', 'SSE 实时面板', '原生 EventSource 驱动进度环与日志流，无需 WebSocket 或轮询'],
  ['timer', 'robfig/cron 定时', '标准 5 段表达式解析，面板内图形化管理与下次执行预览'],
  ['shield', 'AES-256-GCM + bcrypt', 'Cookie 与推送 Token 加密落库，面板密码哈希存储'],
  ['spark', '原生 ES Module 前端', 'go:embed 内嵌静态资源，无构建工具链，保证“单二进制”目标'],
];

export async function renderAbout(ctx) {
  const { root } = ctx;
  root.innerHTML = `
    ${pageHeaderHTML({ title: '关于' })}
    <div class="settings-grid">
      <div>
        <article class="card about-hero" style="margin-bottom:16px">
          <div class="about-brand">
            <div class="brand-mark">${icon('bookOpen')}</div>
            <div>
              <div class="card-title">阅迹 · ReadTrace</div>
              <div class="card-sub">每一次阅读，都留下痕迹。</div>
            </div>
          </div>
          <p class="hint" style="font-size:12.5px;line-height:1.8;margin-top:12px">
            一个单文件、可自托管的微信读书阅读面板。扫码登录，实时看进度，自动跑定时，
            把阅读时长和数据都留在自己手里。数据完全保存在本地，不需要数据库服务，
            把二进制和 <code>data/</code> 目录带走即可迁移。
          </p>
          <div class="about-links">
            <a class="btn btn-primary" href="${REPO_URL}" target="_blank" rel="noopener">
              ${icon('external')}项目地址（GitHub）
            </a>
            <a class="btn" href="${REPO_URL}/issues" target="_blank" rel="noopener">
              ${icon('alert')}问题反馈
            </a>
          </div>
        </article>

        <article class="card">
          <div class="card-head"><div><div class="card-title">${icon('download')}构建</div><div class="card-sub">需要 Go 1.27+</div></div></div>
          <pre class="about-cmd">go build -trimpath -ldflags "-s -w" -o readtrace ./cmd/readtrace</pre>
          <div class="hint">Windows 生成 <code>readtrace.exe</code>；交叉编译加 <code>CGO_ENABLED=0 GOOS=linux GOARCH=amd64</code>，详见 README。</div>
        </article>
      </div>

      <div>
        <article class="card">
          <div class="card-head"><div><div class="card-title">${icon('server')}技术栈</div><div class="card-sub">保持单文件与零外部依赖服务</div></div></div>
          <div class="stack-list">
            ${STACK.map(([ic, name, desc]) => `
              <div class="stack-item">
                ${icon(ic)}
                <div class="stack-text"><b>${name}</b><span>${desc}</span></div>
              </div>`).join('')}
          </div>
          <div class="kv" style="margin-top:10px"><span>开源协议</span><b><a href="${REPO_URL}/blob/main/LICENSE" target="_blank" rel="noopener">AGPL-3.0</a></b></div>
        </article>
      </div>
    </div>
  `;
}
