/**
 * WorkBuddy2APIPanel · magpie 供应商插件
 *
 * 作用：把 WorkBuddy2API 网关接进 magpie —— 填一把网关 api_key，
 * 既有模型可用，用量页也能看到账号池余额。
 *
 * 余额来源：网关自身的 GET /v1/balance（与对话接口同一把 key 鉴权）。
 * 该端点只在带补丁的构建里存在；缺失时降级为「不显示余额」，不影响对话。
 *
 * 可覆盖选项（plugins.json 的 options）：
 *   apiBase         网关地址，必填，例如 https://gw.example.com/v1
 *   balanceUrl      余额端点，默认为 `${apiBase}/balance`
 *   models          模型表，合并进默认表
 */

const PROVIDER = "workbuddy2apipanel";

/**
 * 网关地址必须由使用者自己填：
 * `magpie plugin options workbuddy2apipanel '{"apiBase":"https://自己的网关/v1"}'`。
 *
 * 故意不内置默认值 —— 否则所有人的流量都会打到同一个网关。
 */

/** 把用户填的地址规整成 https://host/v1 形式；空值返回空串。 */
function normalizeBase(input) {
  const raw = String(input ?? "").trim();
  if (!raw) return "";
  let s = raw;
  if (!/^https?:\/\//i.test(s)) s = "https://" + s;
  s = s.replace(/\/+$/, "");
  // 常见误填：填了 /v1/chat/completions 或 /v1/balance 这类具体端点
  s = s.replace(/\/(chat\/completions|completions|models|balance|responses|messages)$/i, "");
  if (!/\/v1$/i.test(s) && !/\/api\/v\d+$/i.test(s)) s += "/v1";
  return s;
}

/**
 * 未登录/拉取失败时的兜底模型表。
 * 权威来源是网关的 GET /v1/models —— 登录后会动态覆盖（见 provider.models）。
 * 这里只放一个占位，避免「0 个模型」让供应商不出现在列表里。
 */
const DEFAULT_MODELS = {
  "cn:fast-model": { name: "Fast (CN)", limit: { context: 256_000, output: 32_000 } },
};

/** 从网关 /v1/models 拉当前账号可用的模型；失败时返回 null（由调用方回退）。 */
async function fetchModels(base, key, signal) {
  try {
    const res = await fetch(`${base}/models`, {
      headers: { authorization: `Bearer ${key}` },
      signal: signal ?? AbortSignal.timeout(8_000),
    });
    if (!res.ok) return null;
    const body = await res.json();
    const list = Array.isArray(body?.data) ? body.data : [];
    if (!list.length) return null;
    return list;
  } catch {
    return null;
  }
}

const trimSlash = (s) => String(s || "").replace(/\/+$/, "");

/** 把 token 数说短：11.9M / 812k / 37。 */
function shortTokens(n) {
  const v = Number(n) || 0;
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)}B`;
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)}M`;
  if (v >= 1e3) return `${Math.round(v / 1e3)}k`;
  return String(Math.round(v));
}

/**
 * 把 /v1/usage/daily 的 today 拼成一行中文文案，放进 magpie 的额度卡。
 * 全部为 0 时返回空串（当天没调用就不占一行）。
 */
function todayLine(t) {
  if (!t || typeof t !== "object") return "";
  const req = Number(t.requests) || 0;
  const tok = Number(t.total_tokens) || 0;
  const cr = Number(t.credits) || 0;
  if (!req && !tok && !cr) return "";
  const parts = [`${req} 次`, `${shortTokens(tok)} tokens`];
  if (cr > 0) parts.push(`${cr.toFixed(2)} credits`);
  const err = Number(t.errors) || 0;
  if (err > 0) parts.push(`${err} 失败`);
  return parts.join(" · ");
}

/**
 * 拉网关的按日用量；返回 today 对象或 null。
 * 端点不存在（404/405）、超时、鉴权外的任何错误都静默返回 null ——
 * 用量面板缺失不应影响账号可用性。
 */
async function fetchDaily(url, key, days) {
  try {
    const res = await fetch(`${trimSlash(url)}?days=${days}`, {
      headers: { authorization: `Bearer ${key}` },
      signal: AbortSignal.timeout(8_000),
    });
    if (!res.ok) return null;
    const body = await res.json().catch(() => null);
    return body?.today ?? null;
  } catch {
    return null;
  }
}


/** 从登录信息里取 key：API key 方式用 key，OAuth 方式用 access。 */
function keyOf(auth) {
  if (!auth) return "";
  return auth.type === "api" ? auth.key || "" : auth.access || "";
}

const WorkBuddy2APIPanel = async (_input, options = {}) => {
  // apiBase 必须由用户配置（options）；没填时下面的 loader/usage 会明确报错。
  const configured = normalizeBase(options.apiBase);
  const base = configured;
  // 余额端点在配置了自定义网关时，默认跟随该网关
  const balanceUrl = options.balanceUrl || `${base}/balance`;
  // 按日用量端点：网关打补丁后才有（GET /v1/usage/daily）。缺失时静默跳过。
  const dailyUrl = options.usageUrl || `${base}/usage/daily`;
  const dailyDays = Number.isFinite(options.usageDays) ? Math.min(90, Math.max(1, options.usageDays)) : 7;
  const models = { ...DEFAULT_MODELS, ...(options.models || {}) };

  // 面板地址：供应商卡片上的「打开」按钮会跳到这里，默认由网关地址推导（https://host/panel/），
  // 也可单独配 consoleUrl。没配 apiBase 时留空，不猜一个别人的网关。
  const derivedOrigin = (() => {
    if (!base) return "";
    try { return new URL(base).origin + "/panel/"; } catch { return ""; }
  })();
  const consoleUrl = options.consoleUrl || derivedOrigin;

  return {
    config: async (cfg) => {
      cfg.provider ??= {};
      // 网关是 OpenAI 兼容的 chat completions 形态。
      cfg.provider[PROVIDER] ??= {
        name: "WorkBuddy2APIPanel",
        npm: "@ai-sdk/openai-compatible",
        api: base,
        models: Object.fromEntries(
          Object.entries(models).map(([id, m]) => [id, { id, tool_call: true, ...m }]),
        ),
        // 详情页标题右侧的「网址 ↗」按钮由 provider 的 website 驱动；两种都只在有地址时给。
        ...(consoleUrl ? { keysUrl: consoleUrl, website: consoleUrl } : {}),
      };
    },

    auth: {
      provider: PROVIDER,
      methods: [{ type: "api", label: "WorkBuddy2API key", placeholder: "网关 api_key" }],

      /** 每个请求都带上这把 key 发往网关。 */
      async loader(getAuth) {
        const auth = await getAuth();
        const key = keyOf(auth);
        if (!key) return {};
        if (!configured) {
          // 发布给别人用时，地址必须由使用者自己填，避免所有人的请求都打到同一个网关。
          throw new Error(
            "workbuddy2apipanel: 未配置 API 地址。请先设置：" +
            `magpie plugin options workbuddy2apipanel '{"apiBase":"https://你的网关/v1"}'`
          );
        }
        // 只给 apiKey —— magpie 会按 AI SDK 的方式发 Authorization: Bearer。
        // 不再自定义 fetch，避免与 apiKey 重复设置同一个头。
        return { baseURL: base, apiKey: key };
      },

      /** magpie 的用量钩子：直接返回网关给出的 usage 结构。 */
      async usage(getAuth) {
        const auth = await getAuth();
        const key = keyOf(auth);
        if (!key) return { error: "missing api key", windows: [] };
        if (!configured) {
          return { error: "未配置 API 地址（见 plugin options）", windows: [] };
        }
        try {
          const res = await fetch(balanceUrl, {
            headers: { authorization: `Bearer ${key}` },
            signal: AbortSignal.timeout(10_000),
          });
          const body = await res.json().catch(() => null);

          if (res.status === 401 || res.status === 403) {
            // 真正的鉴权失败：交给 magpie 标记账号，不吞信号。
            return {
              error: body?.error?.message || `unauthorized (${res.status})`,
              windows: [],
            };
          }
          if (!res.ok) {
            // 404/405/5xx：端点不存在或暂时不可用 —— 静默降级为「无余额面板」，
            // 不能让余额缺失影响账号可用性（也可能只是网关还没打补丁）。
            return { windows: [] };
          }
          const u = body?.usage ?? body ?? {};
          const windows = Array.isArray(u.windows) ? [...u.windows] : [];

          // 今日用量：只作展示，aside 让它不参与路由与占比计算。
          // 端点缺失（网关没打补丁）或拉取失败时，静默不加这一行。
          const today = await fetchDaily(dailyUrl, key, dailyDays);
          const line = todayLine(today);
          if (line) {
            windows.push({
              name: "今日用量",
              display: line,
              unit: "tokens",
              // used / limit / balance 都不给 —— magpie 把它渲染成纯文本行，
              // 不画进度条，也不进 allowanceOf（见 plugin_usage.go 的 Text 分支）。
              aside: true,
            });
          }

          // 仅当网关明确返回了数据时才声明登录有效，避免掩盖吊销。
          return {
            ...(u.error ? {} : { signIn: "kept" }),
            plan: u.plan,
            user: u.user,
            balance: u.balance,
            windows,
            error: u.error || "",
          };
        } catch (e) {
          return { error: `balance unavailable: ${e?.message ?? e}`, windows: [] };
        }
      },
    },

    provider: {
      id: PROVIDER,
      async models(props, ctx) {
        // magpie 传 { models, auth }；权威模型表来自网关 /v1/models。
        const declared = props?.models ?? props ?? DEFAULT_MODELS;
        const key = keyOf(props?.auth ?? ctx?.auth);
        if (!key) return declared;
        const list = await fetchModels(base, key);
        if (!list) return declared; // 拉取失败：保留原有列表，不影响可用性
        const out = {};
        for (const m of list) {
          const id = m?.id;
          if (!id) continue;
          out[id] = {
            ...(declared[id] ?? {}),
            id,
            name: m?.name || id,
            tool_call: true,
            limit: m?.limit ?? declared[id]?.limit ?? { context: 256_000, output: 32_000 },
          };
        }
        return Object.keys(out).length ? out : declared;
      },
    },
  };
};

// magpie 会把入口模块的每个具名函数都当插件调用一次；
// 把工厂收进对象，避免重复注册同一个 provider。
export const _internal = { WorkBuddy2APIPanel };

export default { id: "opencode-workbuddy2apipanel", server: WorkBuddy2APIPanel };
