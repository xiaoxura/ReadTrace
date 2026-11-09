import { icon } from '../icons.js';
import { pageHeaderHTML } from '../util.js';

// 更新日志是纯静态页：不请求接口，版本与条目直接维护在这里。
// 新增版本时把上一版条的 current 去掉，并在数组最前面插入新版本即可。
const CURRENT_VERSION = 'V0.1.0';

const RELEASES = [
  {
    version: 'V0.1.0',
    date: '2026-11-09',
    tag: '首个公开版本',
    current: true,
    intro:
      '阅迹以「Go 单二进制 + SQLite + 内嵌前端」的形态发布：扫码登录、实时阅读、书池管理、'
      + '可视化定时与多渠道推送全部内建，阅读数据完整留在本地。',
    groups: [
      {
        icon: 'qr',
        title: '账号与登录',
        items: [
          '<b>微信扫码绑定</b>：面板直接申请登录二维码，手机确认即完成登录；支持手机端 4 位验证码，无需抓包。',
          '<b>Cookie / cURL 导入</b>：粘贴浏览器里的 read 请求或 curl 命令即可添加账号。',
          '<b>多账号管理</b>：每个账号独立保存默认时长、每日目标、心跳间隔、阅读模式与提醒渠道。',
          '<b>账号指纹模板</b>：每个账号可保存自己的 cURL 抓包模板，签名自动重算；未配置时回落到全局模板。',
          '<b>登录态维护</b>：按设定间隔自动检测（可关闭），Cookie 自动续期；失效账号在面板标注，并可通过绑定的渠道提醒。',
        ],
      },
      {
        icon: 'play',
        title: '阅读引擎',
        items: [
          '<b>三种阅读模式</b>：顺序书单（读完自动下一本、循环回第一本、进度跨次衔接）、随机轮换、指定书籍 + 进度推进。',
          '<b>每日只补不足</b>：按站点时区计算今日已读，已达标直接跳过；未达标只运行「计划时长」与「今日剩余」中的较小值。',
          '<b>失败自动重试</b>：新账号默认重试 5 次、间隔 30 秒，可逐账号调整；登录已失效时不盲目重试。',
          '<b>节奏扰动</b>：心跳间隔带 ±10% 抖动，定时任务触发带 0~90 秒随机延迟，弱化固定请求指纹。',
          '<b>实时面板</b>：SSE 推送进度环、已读分钟、心跳成功/失败次数与完整日志流，运行中可随时停止。',
        ],
      },
      {
        icon: 'book',
        title: '书池',
        items: [
          '<b>书架审查导入</b>：一键拉取书架，先勾选再确认，避免误导入整架书籍。',
          '<b>搜索导入</b>：按书籍 ID 或书名查询添加，可选只导免费章节或全本（已购/会员）。',
          '<b>付费跳过</b>：未购买章节自动跳过，不影响已导入书籍的阅读。',
          '<b>池管理</b>：按账号归属管理，支持排序、参与/不参与开关、批量删除、逐章查看字数与付费标识。',
          '<b>目录清理</b>：对照官方目录一键移除付费未购章节；池为空时禁止发起阅读，不内置任何书籍或模板。',
        ],
      },
      {
        icon: 'chart',
        title: '数据与记录',
        items: [
          '<b>统计总览</b>：今日、本周、累计阅读时长与连续阅读天数。',
          '<b>趋势图</b>：最近 14 天柱状图，随主题切换与窗口缩放自动重绘。',
          '<b>运行记录</b>：保留每次运行的时长、状态与完整日志，支持筛选并直接查看失败原因。',
          '<b>数据保留</b>：运行详情与日志默认保留 90 天（<code>-retain-days</code> 可调，0 为永久），累计统计长期保留。',
        ],
      },
      {
        icon: 'bell',
        title: '定时与推送',
        items: [
          '<b>可视化定时</b>：5 段 cron 表达式、常用时间预设、未来 3 次执行预览、启停开关与下次执行倒计时。',
          '<b>13 种推送渠道</b>：PushPlus、WxPusher、Telegram、ServerChan、Server酱³、Bark、Ntfy、Gotify、PushDeer、飞书、企业微信、钉钉、Apprise，均可发送测试消息。',
          '<b>按账号绑定</b>：每个账号可选择自己的问题提醒渠道，失败与失效消息模板支持 <code>{{account}}</code>、<code>{{elapsed}}</code> 等变量。',
          '<b>安静的问题提醒</b>：正常完成、手动停止、重试后恢复均不推送；仅最终失败或确认失效时提醒，同一失效状态不重复打扰。',
        ],
      },
      {
        icon: 'shield',
        title: '系统与安全',
        items: [
          '<b>单二进制部署</b>：Go + <code>go:embed</code> 内嵌前端，无需 Node/Python 运行时，SQLite 单文件存储。',
          '<b>加密落库</b>：账号 Cookie 与推送 Token 使用 AES-256-GCM 加密，主密钥保存在本机；面板密码使用 bcrypt 哈希。',
          '<b>访问防护</b>：30 天登录会话、同源请求校验、可配置 Host 白名单，以及数据目录实例锁。',
          '<b>迁移与部署</b>：复制 <code>data/</code> 目录即可迁移；提供 Dockerfile 与 systemd 示例。',
          '<b>界面体验</b>：桌面侧边栏 / 移动端底部导航，深色与浅色主题，响应式适配。',
        ],
      },
    ],
  },
];

function releaseHTML(release) {
  return `
    <article class="card release-card${release.current ? ' current' : ''}">
      <div class="release-head">
        <div class="release-heading">
          <div class="release-version">
            ${release.version}
            ${release.current ? '<span class="badge ok">最新</span>' : ''}
          </div>
          <div class="release-meta">${release.tag} · ${release.date}</div>
        </div>
        <div class="release-mark">${icon('rocket')}</div>
      </div>
      <p class="release-intro">${release.intro}</p>
      <div class="release-groups">
        ${release.groups.map((group) => `
          <section class="release-group">
            <div class="release-group-head">${icon(group.icon)}<span>${group.title}</span></div>
            <ul class="release-list">
              ${group.items.map((item) => `<li>${item}</li>`).join('')}
            </ul>
          </section>`).join('')}
      </div>
    </article>`;
}

export async function renderChangelog(ctx) {
  const { root } = ctx;
  root.innerHTML = `
    ${pageHeaderHTML({
      title: '更新日志',
      sub: '记录阅迹的版本变化与功能演进',
      actions: `<span class="badge ok">${icon('spark')}当前版本 ${CURRENT_VERSION}</span>`,
    })}
    <div class="release-list-wrap">
      ${RELEASES.map(releaseHTML).join('')}
    </div>
  `;
}
