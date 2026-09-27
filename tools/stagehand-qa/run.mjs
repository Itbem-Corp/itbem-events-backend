import fs from "node:fs/promises";
import { randomUUID } from "node:crypto";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { Stagehand } from "@browserbasehq/stagehand";
import { z } from "zod";

const maxURLLength = 2048;
const maxSummaryLength = 1200;
const maxGatewayPayloadBytes = 8_192;
const maxBrowserQACases = 3;
const desktopViewport = Object.freeze({ name: "desktop", width: 1440, height: 1200 });
const mobileViewport = Object.freeze({ name: "mobile", width: 412, height: 915 });
// A browser QA job must be bounded even when a provider, a browser launch or
// an underlying transport becomes unhealthy. These are deliberately local
// runner limits: the worker still owns the broader task lease and retry policy.
const stagehandInitializationTimeoutMs = 35_000;
const gatewayRequestTimeoutMs = 30_000;
const stagehandCloseTimeoutMs = 10_000;

export async function withTimeout(operation, timeoutMs, label) {
  if (typeof operation !== "function" || !Number.isInteger(timeoutMs) || timeoutMs < 1) {
    throw new Error("timeout operation is invalid");
  }
  let timer;
  return new Promise((resolve, reject) => {
    timer = setTimeout(() => reject(new Error(`${label} timed out after ${timeoutMs}ms`)), timeoutMs);
    Promise.resolve()
      .then(operation)
      .then(resolve, reject)
      .finally(() => clearTimeout(timer));
  });
}

export function responsiveViewport(name) {
  if (name === "desktop") return { ...desktopViewport };
  if (name === "mobile") return { ...mobileViewport };
  throw new Error("unsupported responsive QA viewport");
}

function supportedNodeRuntime() {
  const [major, minor] = process.versions.node.split(".").map((part) => Number.parseInt(part, 10));
  return major > 22 || (major === 22 && minor >= 12) || (major === 20 && minor >= 19);
}

function fail(message) {
  process.stderr.write(`${message}\n`);
  process.exitCode = 2;
}

export function createPreviewURLContext(rawURL) {
  let navigation;
  try {
    navigation = new URL(String(rawURL ?? "").trim());
  } catch {
    throw new Error("preview URL must be a bounded HTTP(S) URL without credentials");
  }
  if (!/^https?:$/.test(navigation.protocol) || navigation.username || navigation.password || navigation.href.length > maxURLLength) {
    throw new Error("preview URL must be a bounded HTTP(S) URL without credentials");
  }
  const report = new URL(navigation.href);
  report.search = "";
  report.hash = "";
  const sensitiveValues = new Set();
  const addSensitiveValue = (value) => {
    const normalized = String(value ?? "");
    if (normalized.length >= 1) {
      sensitiveValues.add(normalized);
      try { sensitiveValues.add(decodeURIComponent(normalized)); } catch {}
      try { sensitiveValues.add(encodeURIComponent(normalized)); } catch {}
    }
  };
  for (const [, value] of navigation.searchParams) addSensitiveValue(value);
  if (navigation.hash) addSensitiveValue(navigation.hash.slice(1));
  const sortedSensitiveValues = [...sensitiveValues].filter(Boolean).sort((left, right) => right.length - left.length);
  const sanitizeURL = (value) => {
    try {
      const parsed = new URL(String(value ?? ""));
      parsed.username = "";
      parsed.password = "";
      parsed.search = "";
      parsed.hash = "";
      return parsed.href;
    } catch {
      return "";
    }
  };
  const sanitizeText = (value, limit = maxSummaryLength) => {
    let text = String(value ?? "").replace(/\u0000/g, "").slice(0, Math.max(limit * 4, maxURLLength * 2));
    text = text.replace(/https?:\/\/[^\s"'<>]+/gi, (candidate) => {
      const trailing = candidate.match(/[),.;!?]+$/)?.[0] ?? "";
      const url = trailing ? candidate.slice(0, -trailing.length) : candidate;
      return `${sanitizeURL(url) || "[REDACTED_URL]"}${trailing}`;
    });
    for (const secret of sortedSensitiveValues) {
      const escaped = secret.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      text = text.replace(new RegExp(escaped, "gi"), "[REDACTED_URL_VALUE]");
    }
    for (const testValue of approvedTestValues()) text = text.split(testValue).join("[REDACTED_TEST_VALUE]");
    return safeText(text, limit);
  };
  return {
    navigationURL: navigation.href,
    reportURL: report.href,
    sanitizeURL,
    sanitizeText,
  };
}

export async function parseArguments(argv) {
  const values = new Map();
  for (let index = 0; index < argv.length;) {
    const key = argv[index];
    const value = argv[index + 1];
    if ((key !== "--url" && key !== "--url-file" && key !== "--output" && key !== "--plan") || !value || values.has(key)) {
      throw new Error("usage: node run.mjs (--url <http(s) preview> | --url-file <private URL file>) --output <report.json> [--plan <browser-plan.json>]");
    }
    values.set(key, value);
    index += 2;
  }
  const hasLiteralURL = values.has("--url");
  const hasURLFile = values.has("--url-file");
  if (hasLiteralURL === hasURLFile || !values.has("--output")) {
    throw new Error("usage: node run.mjs (--url <http(s) preview> | --url-file <private URL file>) --output <report.json> [--plan <browser-plan.json>]");
  }
  let rawURL;
  if (hasURLFile) {
    try {
      const stat = await fs.stat(path.resolve(values.get("--url-file")));
      if (!stat.isFile() || stat.size > maxURLLength + 1) throw new Error("invalid");
      rawURL = await fs.readFile(path.resolve(values.get("--url-file")), "utf8");
    } catch {
      throw new Error("private preview URL file could not be read");
    }
  } else {
    rawURL = values.get("--url");
    // Literal command-line arguments are observable in process listings and
    // crash diagnostics. Signed query strings and fragments must arrive only
    // through the wrapper-created private file instead.
    if (rawURL.includes("?") || rawURL.includes("#")) {
      throw new Error("signed preview URLs must be supplied with --url-file");
    }
  }
  const urlContext = createPreviewURLContext(rawURL);
  const output = path.resolve(values.get("--output"));
  if (path.extname(output).toLowerCase() !== ".json") {
    throw new Error("semantic QA output must be a .json artifact");
  }
  const plan = values.has("--plan") ? path.resolve(values.get("--plan")) : "";
  if (plan && path.extname(plan).toLowerCase() !== ".json") {
    throw new Error("browser QA plan must be a .json file");
  }
  return { previewURL: urlContext.navigationURL, reportURL: urlContext.reportURL, urlContext, output, plan };
}

// Kept as a tiny injection seam so tests can assert the exact URL handed to
// the browser without starting Chromium or Stagehand.
export async function navigatePreview(page, previewURL) {
  if (!page || typeof page.goto !== "function") throw new Error("Stagehand browser page is unavailable");
  await page.goto(previewURL, { waitUntil: "domcontentloaded", timeoutMs: 45_000 });
}

function environment() {
  const value = (process.env.STAGEHAND_QA_ENV ?? "LOCAL").trim().toUpperCase();
  if (value !== "LOCAL" && value !== "BROWSERBASE") {
    throw new Error("STAGEHAND_QA_ENV must be LOCAL or BROWSERBASE");
  }
  return value;
}

export function modelConfiguration(env) {
  const inferenceURL = (process.env.STAGEHAND_QA_INFERENCE_URL ?? "").trim();
  const inferenceCapability = (process.env.STAGEHAND_QA_INFERENCE_CAPABILITY ?? "").trim();
  const taskID = (process.env.STAGEHAND_QA_TASK_ID ?? "").trim();
  const runID = (process.env.STAGEHAND_QA_RUN_ID ?? "").trim();
  const operation = (process.env.STAGEHAND_QA_OPERATION ?? "").trim();
  if (!inferenceURL || !inferenceCapability || !taskID || !runID || operation !== "delivery.qa") {
    throw new Error("Stagehand requires a bound delivery.qa inference gateway lease");
  }
  if (env === "BROWSERBASE" && !(process.env.BROWSERBASE_API_KEY ?? "").trim()) {
    throw new Error("BROWSERBASE_API_KEY is required when STAGEHAND_QA_ENV=BROWSERBASE");
  }
  let endpoint;
  try {
    endpoint = new URL(inferenceURL);
  } catch {
    throw new Error("Stagehand QA inference gateway URL is invalid");
  }
  const loopback = endpoint.hostname === "localhost" || endpoint.hostname === "127.0.0.1" || endpoint.hostname === "::1";
  if ((endpoint.protocol !== "https:" && !(endpoint.protocol === "http:" && loopback)) || endpoint.username || endpoint.password || endpoint.search || endpoint.hash) {
    throw new Error("Stagehand QA inference gateway must use HTTPS or loopback HTTP");
  }
  // The runner uses only deterministic browser APIs. Its semantic call below
  // goes to ITBEM's gateway; this model-shaped value deliberately cannot grant
  // a provider credential to Stagehand or a customer repository.
  return {
    modelName: "openai/itbem-gateway",
    // Stagehand requires an API-key-shaped config value, but the semantic
    // path below uses only this run-scoped, inference-only capability.
    apiKey: "itbem-gateway-local-only",
    baseURL: endpoint.href.replace(/\/$/, ""),
    inferenceURL: endpoint.href,
    inferenceCapability,
    taskID,
    runID,
    operation,
    provider: "gateway",
    ledgerModel: "gateway-managed",
    isGateway: true,
    openaiEndpointFormat: "chat",
  };
}

export function sendGatewayInference(model, payload, signal, fetchImpl = fetch) {
  if (!isUUID(payload?.call_id)) throw new Error("Stagehand gateway call_id must be a UUID");
  let encodedPayload;
  try {
    encodedPayload = JSON.stringify(payload);
  } catch {
    throw new Error("Stagehand gateway payload could not be encoded");
  }
  if (Buffer.byteLength(encodedPayload, "utf8") > maxGatewayPayloadBytes) {
    throw new Error("Stagehand gateway payload exceeds the privacy size limit");
  }
  return fetchImpl(model.inferenceURL, {
    method: "POST",
    headers: {
      ["X-ITBEM-Inference-Capability"]: model.inferenceCapability,
      "Content-Type": "application/json",
    },
    body: encodedPayload,
    signal,
    // A bearer capability is scoped to this gateway request. Do not let a
    // redirect carry it to another origin (or any redirect target).
    redirect: "error",
  });
}

function isUUID(value) {
  return typeof value === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value);
}

function safeText(value, limit = maxSummaryLength) {
  return String(value ?? "").replace(/[\u0000-\u001f\u007f]/g, " ").replace(/\s+/g, " ").trim().slice(0, limit);
}

function boundedPrivateText(value, limit = 16_000) {
  return String(value ?? "").replace(/\u0000/g, "").slice(0, limit);
}

async function browserViewport(page) {
  try {
    const viewport = await page.evaluate(() => ({
      width: Math.max(0, Math.trunc(window.innerWidth || 0)),
      height: Math.max(0, Math.trunc(window.innerHeight || 0)),
      device_scale_factor: Math.max(0, Number(window.devicePixelRatio || 0)),
    }));
    return viewport;
  } catch {
    return { width: 0, height: 0, device_scale_factor: 0 };
  }
}

async function setVerifiedViewport(page, requested) {
  // Stagehand v3 accepts width and height as separate arguments. Verify the
  // browser-reported CSS viewport afterwards so an API mismatch can never be
  // mislabeled as responsive evidence in a human QA gate.
  await page.setViewportSize(requested.width, requested.height, { deviceScaleFactor: 1 });
  const actual = await browserViewport(page);
  if (Math.abs(actual.width - requested.width) > 2 || Math.abs(actual.height - requested.height) > 2) {
    throw new Error(`Stagehand did not apply the requested ${requested.name} viewport`);
  }
  return actual;
}

export function safeMetrics(metrics) {
  const source = metrics && typeof metrics === "object" ? metrics : {};
  const numeric = (name) => Number.isFinite(source[name]) ? Math.max(0, Math.trunc(source[name])) : 0;
  const input = numeric("totalPromptTokens");
  const output = numeric("totalCompletionTokens");
  return {
    input_tokens: input,
    output_tokens: output,
    reasoning_tokens: numeric("totalReasoningTokens"),
    cached_input_tokens: numeric("totalCachedInputTokens"),
    cache_write_tokens: numeric("totalCacheCreationInputTokens") || numeric("totalCacheWriteInputTokens"),
    // MiniMax reports reasoning as a detail of completion usage. Keep it as
    // its own observability dimension, but do not count it again in the
    // aggregate token total or dashboard usage would be inflated.
    total_tokens: input + output,
    inference_ms: numeric("totalInferenceTimeMs"),
  };
}

export function safeProviderUsage(usage) {
  const source = usage && typeof usage === "object" ? usage : {};
  const numeric = (...values) => {
    const value = values.find(Number.isFinite);
    return value === undefined ? 0 : Math.max(0, Math.trunc(value));
  };
  // The gateway returns provider-native usage keys (snake_case), while the
  // Stagehand SDK's own metrics use camelCase. Accept both forms here so a
  // valid receipt does not become a zero-token local report. The nested
  // details below are provider-native MiniMax/OpenAI/Anthropic usage fields.
  const input = numeric(source.inputTokens, source.input_tokens, source.prompt_tokens);
  const output = numeric(source.outputTokens, source.output_tokens, source.completion_tokens);
  const reasoning = numeric(
    source.reasoningTokens,
    source.reasoning_tokens,
    source.output_tokens_details?.reasoning_tokens,
    source.completion_tokens_details?.reasoning_tokens,
  );
  const cached = numeric(
    source.cachedInputTokens,
    source.cached_input_tokens,
    source.input_tokens_details?.cached_tokens,
    source.prompt_tokens_details?.cached_tokens,
    source.cache_read_input_tokens,
  );
  const cacheWrite = numeric(
    source.cacheCreationInputTokens,
    source.cacheWriteInputTokens,
    source.cache_creation_input_tokens,
    source.cache_write_tokens,
  );
  return {
    input_tokens: input,
    output_tokens: output,
    reasoning_tokens: reasoning,
    cached_input_tokens: cached,
    cache_write_tokens: cacheWrite,
    // Provider total_tokens normally already includes reasoning within output.
    // Fall back to the two billable aggregate dimensions, never their sum
    // plus reasoning, so cost and displayed total remain consistent.
    total_tokens: Math.max(numeric(source.totalTokens, source.total_tokens), input + output),
    inference_ms: 0,
  };
}

export function resolvedUsage(metrics, providerUsage) {
  const stagehandUsage = safeMetrics(metrics);
  const fallback = safeProviderUsage(providerUsage);
  return {
    input_tokens: stagehandUsage.input_tokens || fallback.input_tokens,
    output_tokens: stagehandUsage.output_tokens || fallback.output_tokens,
    reasoning_tokens: stagehandUsage.reasoning_tokens || fallback.reasoning_tokens,
    cached_input_tokens: stagehandUsage.cached_input_tokens || fallback.cached_input_tokens,
    cache_write_tokens: stagehandUsage.cache_write_tokens || fallback.cache_write_tokens,
    total_tokens: Math.max(stagehandUsage.total_tokens, fallback.total_tokens),
    inference_ms: stagehandUsage.inference_ms,
  };
}

export function combinedUsage(...usages) {
  return usages.filter(Boolean).reduce((total, usage) => ({
    input_tokens: total.input_tokens + (usage.input_tokens ?? 0),
    output_tokens: total.output_tokens + (usage.output_tokens ?? 0),
    reasoning_tokens: total.reasoning_tokens + (usage.reasoning_tokens ?? 0),
    cached_input_tokens: total.cached_input_tokens + (usage.cached_input_tokens ?? 0),
    cache_write_tokens: total.cache_write_tokens + (usage.cache_write_tokens ?? 0),
    total_tokens: total.total_tokens + (usage.total_tokens ?? 0),
    inference_ms: total.inference_ms + (usage.inference_ms ?? 0),
  }), {
    input_tokens: 0,
    output_tokens: 0,
    reasoning_tokens: 0,
    cached_input_tokens: 0,
    cache_write_tokens: 0,
    total_tokens: 0,
    inference_ms: 0,
  });
}

const PageAssessment = z.object({
  // Page-derived text is intentionally not part of the gateway contract.
  // These empty strings preserve the current report shape without allowing
  // the model to echo or invent user-visible content.
  title: z.string().max(0),
  primary_heading: z.string().max(0),
  primary_action: z.string().max(0),
  blocking_issue: z.enum(["", "browser_e2e_failed", "responsive_overflow", "browser_runtime_errors", "network_observation_missing"]),
}).strict();

const semanticSystemPrompt = [
  "You are a careful read-only web QA reviewer.",
  "Use only the supplied categorical browser signals. They contain no page text by design.",
  "Return one JSON object and nothing else.",
  "It must have exactly these string keys: title, primary_heading, primary_action, blocking_issue.",
  "title, primary_heading, and primary_action must always be empty strings.",
  "blocking_issue must be one of: empty string, browser_e2e_failed, responsive_overflow, browser_runtime_errors, network_observation_missing.",
  "Never return, infer, or request visible page text, credentials, private data, or hidden state.",
].join(" ");

function approvedTestValues() {
  return Object.entries(process.env)
    .filter(([name, value]) => /^ITBEM_QA_[A-Z0-9_]{1,60}$/.test(name) && typeof value === "string" && value.length >= 3)
    .map(([, value]) => value)
    .sort((left, right) => right.length - left.length);
}

export function redactedInferenceText(value, limit = 6000, previewURLContext = null) {
  let redacted = previewURLContext
    ? previewURLContext.sanitizeText(value, Math.max(limit * 4, maxURLLength * 2))
    : boundedPrivateText(value, Math.max(limit * 4, maxURLLength * 2));
  redacted = redacted
    .replace(/\b(?:sk|pk|rk|AKIA)[-_a-zA-Z0-9]{16,}\b/g, "[REDACTED_SECRET]")
    .replace(/\b(bearer|token|api[_ -]?key|password|secret)\s*[:=]\s*\S+/gi, "$1=[REDACTED]")
    .replace(/data:[^\s]{1,4096}/gi, "[REDACTED_DATA_URL]");
  for (const testValue of approvedTestValues()) redacted = redacted.split(testValue).join("[REDACTED_TEST_VALUE]");
  return safeText(redacted, limit);
}

// Known-secret patterns are a secondary defense only; they cannot classify
// arbitrary user-visible content. Gateway evidence is built from an explicit
// categorical allowlist below, never from these generic string helpers.
export function redactedInferenceEvidence(value, depth = 0, previewURLContext = null) {
  // Generic recursive redaction cannot establish that arbitrary strings are
  // non-sensitive. Callers must use the explicit categorical projection in
  // browserSemanticEvidence instead.
  void value;
  void depth;
  void previewURLContext;
  return "[OMITTED_UNTRUSTED_EVIDENCE]";
}

export function sanitizeReportValue(value, previewURLContext, depth = 0) {
  if (depth > 12) return "[TRUNCATED_REPORT]";
  if (typeof value === "string") return redactedInferenceText(value, 16_000, previewURLContext);
  if (Array.isArray(value)) return value.map((entry) => sanitizeReportValue(entry, previewURLContext, depth + 1));
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([key, entry]) => [key, sanitizeReportValue(entry, previewURLContext, depth + 1)]));
  }
  return value;
}

function parseGatewayAssessment(content) {
  const text = boundedPrivateText(content, 10_000)
    .replace(/<think>[\s\S]*?<\/think>/gi, "")
    .replace(/^\s*```(?:json)?\s*/i, "")
    .replace(/\s*```\s*$/i, "")
    .trim();
  const start = text.indexOf("{");
  const end = text.lastIndexOf("}");
  if (start < 0 || end <= start) throw new Error("Gateway did not return a JSON object");
  const parsed = JSON.parse(text.slice(start, end + 1));
  const validated = PageAssessment.safeParse(parsed);
  if (!validated.success) throw new Error("Gateway JSON did not match the categorical QA assessment contract");
  return validated.data;
}

export function miniMaxUsage(response, statusCode = 0) {
  const usage = response?.usage ?? {};
  const normalized = safeProviderUsage({
    inputTokens: usage.prompt_tokens,
    outputTokens: usage.completion_tokens,
    reasoningTokens: usage.completion_tokens_details?.reasoning_tokens,
    cachedInputTokens: usage.prompt_tokens_details?.cached_tokens,
    totalTokens: usage.total_tokens,
  });
  const choice = Array.isArray(response?.choices) ? response.choices[0] : null;
  const finishReason = safeText(choice?.finish_reason, 64);
  const baseStatus = Number.isFinite(response?.base_resp?.status_code) ? Math.trunc(response.base_resp.status_code) : 0;
  const httpStatus = Number.isInteger(statusCode) && statusCode >= 100 && statusCode <= 999 ? statusCode : 0;
  const providerOutcome = {
    ...(finishReason && /^[A-Za-z0-9._:-]{1,64}$/.test(finishReason) ? { finish_reason: finishReason } : {}),
    ...(response?.usage?.input_sensitive === true ? { input_sensitive: true } : {}),
    ...(response?.usage?.output_sensitive === true ? { output_sensitive: true } : {}),
    ...(httpStatus || (baseStatus >= 100 && baseStatus <= 999) ? { status_code: httpStatus || baseStatus } : {}),
  };
  return Object.keys(providerOutcome).length > 0 ? { ...normalized, _itbem_provider: providerOutcome } : normalized;
}

function boundedSignalCount(value) {
  return Number.isFinite(value) ? Math.min(10_000, Math.max(0, Math.trunc(value))) : 0;
}

function summarizeBrowserE2E(value) {
  const source = value && typeof value === "object" ? value : {};
  const cases = Array.isArray(source.cases) ? source.cases.slice(0, maxBrowserQACases) : [];
  const steps = cases.flatMap((testCase) => Array.isArray(testCase?.steps) ? testCase.steps.slice(0, 8) : []);
  const modes = new Set(["read_only", "approved_navigation", "approved_test_flow"]);
  return {
    mode: modes.has(source.mode) ? source.mode : "read_only",
    passed: source.passed === true,
    case_count: cases.length,
    step_count: steps.length,
    passed_step_count: steps.filter((step) => step?.passed === true).length,
    failed_step_count: steps.filter((step) => step?.passed === false).length,
  };
}

function summarizeResponsive(value) {
  const source = value && typeof value === "object" ? value : {};
  const overflow = source.overflow && typeof source.overflow === "object" ? source.overflow : null;
  const hasOverflowMeasurement = Number.isFinite(overflow?.document_width) && Number.isFinite(overflow?.viewport_width);
  return {
    passed: source.passed === true,
    viewport: "mobile",
    overflow_measured: hasOverflowMeasurement,
    horizontal_overflow: hasOverflowMeasurement && overflow.document_width > overflow.viewport_width + 2,
    screenshot_omitted: true,
  };
}

function summarizeBrowserRuntime(value) {
  const source = value && typeof value === "object" ? value : {};
  const consoleErrorCount = Array.isArray(source.console_errors) ? source.console_errors.length : 0;
  const failedRequestCount = Array.isArray(source.failed_requests) ? source.failed_requests.length : 0;
  const runtimeSignals = {
    console_errors: Array.from({ length: boundedSignalCount(consoleErrorCount) }, () => "console_error"),
    failed_requests: Array.from({ length: boundedSignalCount(failedRequestCount) }, () => "failed_request"),
    observed_network_sources: Array.isArray(source.observed_network_sources) ? source.observed_network_sources : [],
  };
  return {
    passed: browserRuntimePassed(runtimeSignals),
    console_error_count: boundedSignalCount(consoleErrorCount),
    failed_request_count: boundedSignalCount(failedRequestCount),
    has_network_observation: browserRuntimeHasNetworkObservation(runtimeSignals),
    network_sources: runtimeSignals.observed_network_sources.filter((entry) => entry === "response_event" || entry === "performance_timing"),
  };
}

export async function browserSemanticEvidence(page, executionEvidence = {}) {
  let observed = {};
  try {
    observed = await page.evaluate(() => {
      const visible = (element) => {
        if (!element) return false;
        const style = getComputedStyle(element);
        const rect = element.getBoundingClientRect();
        return style.visibility !== "hidden" && style.display !== "none" && Number(style.opacity || 1) > 0 && rect.width > 0 && rect.height > 0;
      };
      const headings = document.querySelectorAll("h1, [role='heading'][aria-level='1']");
      const actions = document.querySelectorAll("button, [role='button'], a[href]");
      return {
        document_ready: document.readyState === "interactive" || document.readyState === "complete",
        title_present: Boolean(document.title),
        body_has_elements: Boolean(document.body?.childElementCount),
        primary_heading_count: headings.length,
        visible_primary_heading_count: Array.from(headings).filter(visible).length,
        primary_action_count: actions.length,
        visible_primary_action_count: Array.from(actions).filter(visible).length,
        form_count: document.forms.length,
        input_count: document.querySelectorAll("input, textarea, select").length,
      };
    });
  } catch {
    observed = {};
  }
  // Explicit projection is the boundary: do not recursively sanitize or pass
  // through any browser-, plan-, or provider-originated strings.
  return {
    page: {
      inspection_available: Object.keys(observed).length > 0,
      document_ready: observed.document_ready === true,
      title_present: observed.title_present === true,
      body_has_elements: observed.body_has_elements === true,
      primary_heading_count: boundedSignalCount(observed.primary_heading_count),
      visible_primary_heading_count: boundedSignalCount(observed.visible_primary_heading_count),
      primary_action_count: boundedSignalCount(observed.primary_action_count),
      visible_primary_action_count: boundedSignalCount(observed.visible_primary_action_count),
      form_count: boundedSignalCount(observed.form_count),
      input_count: boundedSignalCount(observed.input_count),
    },
    approved_browser_e2e: summarizeBrowserE2E(executionEvidence.browser_e2e),
    responsive: summarizeResponsive(executionEvidence.responsive),
    browser_runtime: summarizeBrowserRuntime(executionEvidence.browser_runtime),
  };
}

export async function assessWithGateway(page, model, executionEvidence, fetchImpl = fetch) {
  const evidence = await browserSemanticEvidence(page, executionEvidence);
  const callID = randomUUID();
  const requestPayload = {
    // These are transport placeholders only. Infer ignores them and resolves
    // the effective provider/model from the persisted delivery.qa route.
    provider: "minimax",
    model: "gateway-managed",
    messages: [
      { role: "system", content: semanticSystemPrompt },
      { role: "user", content: `Categorical browser QA signals (read-only):\n${JSON.stringify(evidence)}` },
    ],
    max_completion_tokens: 1024,
    call_id: callID,
    task_id: model.taskID,
    run_id: model.runID,
    operation: model.operation,
  };
  const requestAudit = { endpoint: model.inferenceURL, call_id: callID, body: requestPayload };
  let response;
  const controller = new AbortController();
  const requestTimeout = setTimeout(() => controller.abort(new Error(`Gateway request timed out after ${gatewayRequestTimeoutMs}ms`)), gatewayRequestTimeoutMs);
  try {
    response = await sendGatewayInference(model, requestPayload, controller.signal, fetchImpl);
  } catch {
    throw Object.assign(new Error("Gateway inference transport failed"), {
      usage: safeProviderUsage({}),
      call: { call_key: "semantic-assessment", call_id: callID, call_status: "failed", provider: "gateway", model: "gateway-managed", usage: safeProviderUsage({}), request: requestAudit, response: { transport_error: "gateway_transport_error" } },
    });
  } finally {
    clearTimeout(requestTimeout);
  }
  const payload = await response.json().catch(() => ({}));
  const usage = safeProviderUsage(payload?.usage ?? {});
  const provider = safeText(payload?.provider, 48);
  const providerModel = safeText(payload?.model, 200);
  const call = { call_key: "semantic-assessment", call_id: callID, receipt_id: "", call_status: "completed", provider, model: providerModel, usage, request: requestAudit, response: { status_code: response.status } };
  const receiptCorrelated = payload?.call_id === callID && isUUID(payload?.receipt_id);
  if (receiptCorrelated) call.receipt_id = payload.receipt_id;
  if (!response.ok) {
    call.call_status = "failed";
    throw Object.assign(new Error("Gateway inference rejected the request"), { usage, call });
  }
  if (!receiptCorrelated) {
    call.call_status = "failed";
    throw Object.assign(new Error("Gateway inference receipt correlation failed"), { usage, call });
  }
  const content = payload?.content;
  try {
    return { assessment: parseGatewayAssessment(content), usage, responseExcerpt: "", call };
  } catch {
    call.call_status = "failed";
    throw Object.assign(new Error("Gateway assessment did not match the categorical QA contract"), { usage, call, responseExcerpt: "" });
  }
}

async function fallbackAssessment(page) {
  void page;
  return { title: "", primary_heading: "", primary_action: "", blocking_issue: "" };
}

function safeIdentifier(value, label) {
  const text = String(value ?? "").trim();
  if (!/^[a-z0-9][a-z0-9_-]{0,63}$/i.test(text)) {
    throw new Error(`${label} must be a short identifier`);
  }
  return text;
}

function safeRelativePath(value, label) {
  const text = String(value ?? "").trim();
  if (!text.startsWith("/") || text.startsWith("//") || text.includes("\\") || text.length > 1024) {
    throw new Error(`${label} must be a bounded same-origin path`);
  }
  return text;
}

function safeSelector(value) {
  const text = String(value ?? "").trim();
  if (!text || text.length > 300 || /[\u0000-\u001f\u007f]/.test(text)) {
    throw new Error("browser QA selector is invalid");
  }
  return text;
}

function safeAssertionText(value) {
  const text = String(value ?? "").replace(/[\u0000-\u001f\u007f]/g, " ").replace(/\s+/g, " ").trim();
  if (!text || text.length > 500) {
    throw new Error("browser QA expected text is invalid");
  }
  return text;
}

function safeTestValueReference(value) {
  const text = String(value ?? "").trim();
  if (!/^ITBEM_QA_[A-Z0-9_]{1,60}$/.test(text)) {
    throw new Error("browser QA test value reference is invalid");
  }
  return text;
}

export function browserRuntimePassed(evidence) {
  const source = evidence && typeof evidence === "object" ? evidence : {};
  const consoleErrors = Array.isArray(source.console_errors) ? source.console_errors : [];
  const failedRequests = Array.isArray(source.failed_requests) ? source.failed_requests : [];
  return consoleErrors.length === 0 && failedRequests.length === 0 && browserRuntimeHasNetworkObservation(source);
}

// A passing E2E result requires a real network observation path. Stagehand's
// compact Page API can omit Playwright-like events, so Performance Timing is
// an equally valid Chromium-native fallback; having neither is not evidence
// that no failing request occurred.
export function browserRuntimeHasNetworkObservation(evidence) {
  const sources = Array.isArray(evidence?.observed_network_sources) ? evidence.observed_network_sources : [];
  return sources.some((source) => source === "response_event" || source === "performance_timing");
}

export async function loadBrowserPlan(planPath) {
  if (!planPath) return { schema_version: 1, mode: "read_only", cases: [] };
  const raw = await fs.readFile(planPath, "utf8");
  if (Buffer.byteLength(raw, "utf8") > 24_000) throw new Error("browser QA plan exceeds the size limit");
  let parsed;
  try { parsed = JSON.parse(raw); } catch { throw new Error("browser QA plan must be valid JSON"); }
  if (!parsed || typeof parsed !== "object" || parsed.schema_version !== 1 || !["read_only", "approved_navigation", "approved_test_flow"].includes(parsed.mode) || !Array.isArray(parsed.cases) || parsed.cases.length > maxBrowserQACases) {
    throw new Error("browser QA plan has an unsupported shape");
  }
  // If a browser is already authenticated, visual evidence can expose account
  // data even before a test-flow fill. Plans can declare that state explicitly;
  // approved_test_flow is treated as potentially authenticated regardless.
  if (parsed.uses_authenticated_session !== undefined && typeof parsed.uses_authenticated_session !== "boolean") {
    throw new Error("browser QA authenticated-session flag is invalid");
  }
  const caseIDs = new Set();
  const cases = parsed.cases.map((testCase) => {
    if (!testCase || typeof testCase !== "object") throw new Error("browser QA case is invalid");
    if (testCase.uses_authenticated_session !== undefined && typeof testCase.uses_authenticated_session !== "boolean") {
      throw new Error("browser QA authenticated-session flag is invalid");
    }
    const id = safeIdentifier(testCase.id, "browser QA case id");
    if (caseIDs.has(id)) throw new Error("browser QA case IDs must be unique");
    caseIDs.add(id);
    const title = safeText(testCase.title, 160);
    if (!title || !Array.isArray(testCase.steps) || testCase.steps.length < 1 || testCase.steps.length > 8) throw new Error("browser QA case steps are invalid");
    const steps = testCase.steps.map((step, index) => normalizeBrowserStep(step, parsed.mode, index));
    for (let index = 0; index < steps.length; index += 1) {
      if (steps[index].kind !== "click" || parsed.mode !== "approved_test_flow") continue;
      const postActionAssertion = steps[index + 1];
      if (!postActionAssertion || !["assert_visible", "assert_text", "assert_path"].includes(postActionAssertion.kind)) {
        throw new Error("approved test-flow clicks require an immediate post-action assertion");
      }
    }
    return { id, title, steps, uses_authenticated_session: testCase.uses_authenticated_session === true };
  });
  return { schema_version: 1, mode: parsed.mode, uses_authenticated_session: parsed.uses_authenticated_session === true, cases };
}

function normalizeBrowserStep(step, mode, index) {
  if (!step || typeof step !== "object") throw new Error("browser QA step is invalid");
  const kind = String(step.kind ?? "").trim();
  if (!["navigate", "assert_visible", "assert_text", "click", "fill", "assert_path"].includes(kind)) throw new Error("browser QA step kind is unsupported");
  const normalized = { id: safeIdentifier(step.id ?? `step-${index + 1}`, "browser QA step id"), kind };
  if (kind === "navigate" || kind === "assert_path") normalized.path = safeRelativePath(step.path, "browser QA path");
  if (kind === "assert_visible" || kind === "click") normalized.selector = safeSelector(step.selector);
  if (kind === "assert_text") normalized.text = safeAssertionText(step.text);
  if (kind === "click") {
    if (mode !== "approved_navigation" && mode !== "approved_test_flow") throw new Error("browser QA click requires an approved interaction mode");
    if (mode === "approved_navigation") normalized.expected_path = safeRelativePath(step.expected_path, "browser QA expected path");
    if (mode === "approved_test_flow" && step.expected_path !== undefined) normalized.expected_path = safeRelativePath(step.expected_path, "browser QA expected path");
  }
  if (kind === "fill") {
    if (mode !== "approved_test_flow") throw new Error("browser QA fill requires approved_test_flow mode");
    normalized.selector = safeSelector(step.selector);
    normalized.value_env = safeTestValueReference(step.value_env);
  }
  if (kind === "assert_path" && mode !== "approved_test_flow") throw new Error("browser QA path assertion requires approved_test_flow mode");
  return normalized;
}

function sameOriginURL(pathname, previewURL) {
  const preview = new URL(previewURL);
  const target = new URL(pathname, preview);
  if (target.origin !== preview.origin) throw new Error("browser QA navigation must stay on the preview origin");
  return target.href;
}

export async function runBrowserCases(page, previewURL, plan, directory, browserRuntime, previewURLContext = null) {
  void directory;
  const cases = [];
  let passed = true;
  for (let caseIndex = 0; caseIndex < plan.cases.length; caseIndex += 1) {
    const testCase = plan.cases[caseIndex];
    const steps = [];
    // Arbitrary page/session content cannot be classified as public, so visual
    // artifacts are denied for all plans, not just declared authenticated flows.
    for (let stepIndex = 0; stepIndex < testCase.steps.length; stepIndex += 1) {
      const step = testCase.steps[stepIndex];
      let stepPassed = true;
      let detail = "";
      try {
        if (step.kind === "navigate") {
          await page.goto(sameOriginURL(step.path, previewURL), { waitUntil: "domcontentloaded", timeoutMs: 45_000 });
        } else if (step.kind === "assert_visible") {
          const locator = page.locator(step.selector);
          stepPassed = (await locator.count()) > 0 && await locator.first().isVisible();
          if (!stepPassed) detail = "Expected visible element was not found";
        } else if (step.kind === "assert_text") {
          stepPassed = (await page.locator("body").innerText()).includes(step.text);
          if (!stepPassed) detail = "Expected text was not visible";
        } else if (step.kind === "assert_path") {
          const current = new URL(page.url());
          const preview = new URL(previewURL);
          stepPassed = current.origin === preview.origin && current.pathname === step.path;
          if (!stepPassed) detail = "Expected same-origin path was not reached";
        } else if (step.kind === "fill") {
          const value = process.env[step.value_env];
          const locator = page.locator(step.selector);
          if (typeof value !== "string" || !value || (await locator.count()) !== 1 || !await locator.first().isVisible()) {
            stepPassed = false;
            detail = "Approved test value or unique visible input was unavailable";
          } else {
            await locator.first().fill(value);
          }
        } else if (step.kind === "click") {
          const locator = page.locator(step.selector);
          if ((await locator.count()) !== 1 || !await locator.first().isVisible()) {
            stepPassed = false;
            detail = "Approved navigation target was not uniquely visible";
          } else {
            await locator.first().click();
            await page.waitForLoadState("domcontentloaded", 15_000).catch(() => {});
            if (step.expected_path) {
              const current = new URL(page.url());
              const preview = new URL(previewURL);
              stepPassed = current.origin === preview.origin && current.pathname === step.expected_path;
              if (!stepPassed) detail = "Approved navigation did not reach the expected path";
            }
          }
        }
      } catch {
        stepPassed = false;
        detail = "Browser QA step failed";
      }
      await browserRuntime?.capturePerformance(page);
      if (!stepPassed) passed = false;
      steps.push({ id: `step-${stepIndex + 1}`, kind: step.kind, passed: stepPassed, detail, url: previewURLContext?.sanitizeURL(page.url()) || safeText(page.url(), 1024), usage: { input_tokens: 0, output_tokens: 0, cached_input_tokens: 0, reasoning_tokens: 0, total_tokens: 0 } });
      if (!stepPassed) break;
    }
    const casePassed = steps.length === testCase.steps.length && steps.every((step) => step.passed);
    cases.push({
      id: `case-${caseIndex + 1}`,
      title: `Approved browser case ${caseIndex + 1}`,
      passed: casePassed,
      steps,
      screenshot_status: "omitted_untrusted_page_content",
      screenshot_reason: "omitted_untrusted_page_content",
      screenshot: "",
      screenshot_captured_at: "",
      screenshot_url: "",
      screenshot_viewport: null,
      before_screenshot: "",
      before_screenshot_captured_at: "",
      before_screenshot_url: "",
      before_screenshot_viewport: null,
      evidence_error: "",
    });
    // Stop after a failed case. Continuing into later navigation or approved
    // clicks from an unknown browser state would make the evidence ambiguous.
    if (!casePassed) break;
  }
  return { mode: plan.mode, passed, cases };
}

export function requiresSensitiveScreenshotOmission(plan) {
  // Arbitrary content cannot be proven non-sensitive from a browser plan.
  // Keep screenshots off for every run, not only plans that declare auth.
  void plan;
  return true;
}

async function waitForRenderedDocument(page) {
  // `domcontentloaded` says the document exists, not that Chromium has painted
  // it at the new emulated viewport. Wait for a visible body and two frames so
  // visual evidence is a real render rather than a race-prone empty bitmap.
  const rendered = await page.evaluate(async () => {
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    const body = document.body;
    const style = body ? getComputedStyle(body) : null;
    return Boolean(body && style && style.visibility !== "hidden" && style.display !== "none" && (body.childElementCount > 0 || body.querySelector("img, svg, canvas, [role], input, button")));
  });
  if (!rendered) throw new Error("Mobile document did not render visible page content");
}

// A DOM assertion can pass while a shell transition is still moving the
// content. Capturing at that instant creates misleading before/after evidence
// (for example, a login form whose companion panel has not finished entering).
// Sample a bounded set of layout/visibility signatures and require two stable
// samples before the PNG is recorded. This is browser-derived; it does not ask
// the model to decide whether a screenshot is complete.
async function waitForVisualStability(page) {
  await page.evaluate(async () => {
    const signature = () => {
      const elements = [
        document.body,
        ...Array.from(document.querySelectorAll("main, form, [role='main'], button, input, h1, h2")).slice(0, 32),
      ];
      return JSON.stringify(elements.map((element) => {
        const rect = element.getBoundingClientRect();
        const style = getComputedStyle(element);
        return [
          Math.round(rect.x * 10) / 10,
          Math.round(rect.y * 10) / 10,
          Math.round(rect.width * 10) / 10,
          Math.round(rect.height * 10) / 10,
          style.visibility,
          style.display,
          style.opacity,
          style.transform,
        ];
      }));
    };
    if (document.fonts?.ready) await document.fonts.ready.catch(() => {});
    let previous = signature();
    let stableSamples = 0;
    const deadline = performance.now() + 1500;
    while (performance.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 80));
      const next = signature();
      if (next === previous) {
        stableSamples += 1;
        if (stableSamples >= 2) return;
      } else {
        stableSamples = 0;
        previous = next;
      }
    }
  });
}

// Responsive QA is deterministic and happens in addition to the reviewed
// browser cases: it never clicks or mutates state. The separate mobile image
// and overflow measurement give the human QA gate useful evidence even when
// a plan's feature-specific assertions only apply to the desktop layout.
export async function runMobileResponsiveSmoke(page, previewURL, directory, browserRuntime, previewURLContext = null, options = {}) {
  // The browser plan cannot prove that arbitrary rendered content is safe.
  // Therefore screenshots stay disabled for every run.
  void directory;
  void options;
  const viewport = responsiveViewport("mobile");
  const startedAt = new Date().toISOString();
  let passed = true;
  let detail = "";
  let url = previewURLContext?.sanitizeURL(previewURL) || safeText(previewURL, maxURLLength);
  let overflow = null;
  try {
    await setVerifiedViewport(page, viewport);
    await navigatePreview(page, previewURL);
    await waitForRenderedDocument(page);
    url = previewURLContext?.sanitizeURL(page.url()) || safeText(page.url(), maxURLLength);
    overflow = await page.evaluate(() => ({
      document_width: Math.max(0, Math.trunc(document.documentElement.scrollWidth || 0)),
      viewport_width: Math.max(0, Math.trunc(window.innerWidth || 0)),
    }));
    // A two-pixel tolerance avoids failing a review due to browser fractional
    // layout rounding, while still catching a real horizontal mobile scroll.
    passed = overflow.document_width <= overflow.viewport_width + 2;
    if (!passed) detail = "Mobile viewport has horizontal overflow";
    await browserRuntime?.capturePerformance(page);
  } catch (error) {
    passed = false;
    detail = "Mobile responsive smoke failed";
  }
  return {
    viewport,
    passed,
    detail,
    url,
    overflow,
    screenshot: "",
    screenshot_status: "omitted_untrusted_page_content",
    screenshot_reason: "omitted_untrusted_page_content",
    captured_at: new Date().toISOString(),
    started_at: startedAt,
  };
}

export function boundedBrowserRuntime(previewURLContext = null) {
  const consoleErrors = [];
  const failedRequests = [];
  const unavailableObservers = [];
  const networkSources = [];
  const record = (entries, value) => {
    // Browser-originated messages and URLs can contain arbitrary account or
    // page data. Keep only a fixed event code and never persist the message.
    if (value && entries.length < 12) entries.push(entries === consoleErrors ? "console_error" : "failed_request");
  };
  return {
    recordConsole(message) {
      if (message?.type?.() === "error") record(consoleErrors, true);
    },
    recordPageError(error) {
      record(consoleErrors, Boolean(error));
    },
    recordRequestFailure(request) {
      const failure = request?.failure?.();
      if (failure?.errorText) record(failedRequests, true);
    },
    recordFailedResponse(response) {
      const status = Number(response?.status?.());
      if (!Number.isFinite(status) || status < 400) return;
      record(failedRequests, true);
    },
    async capturePerformance(page) {
      try {
        const entries = await page.evaluate(() => {
          const performanceEntries = [
            ...performance.getEntriesByType("navigation"),
            ...performance.getEntriesByType("resource"),
          ];
          return performanceEntries.slice(-200).map((entry) => ({
            response_status: Number(entry.responseStatus || 0),
          }));
        });
        if (!networkSources.includes("performance_timing")) networkSources.push("performance_timing");
          for (const entry of Array.isArray(entries) ? entries : []) {
          const status = Number(entry?.response_status);
          if (Number.isFinite(status) && status >= 400) {
            record(failedRequests, true);
          }
        }
      } catch {
        if (!unavailableObservers.includes("performance_timing")) unavailableObservers.push("performance_timing");
      }
    },
    attach(page) {
      // Stagehand deliberately exposes a smaller event surface than raw
      // Playwright on some releases. Runtime observation must be additive:
      // an unavailable optional event cannot turn a valid browser run into a
      // failed QA run.
      for (const [event, handler] of [["console", this.recordConsole], ["pageerror", this.recordPageError], ["requestfailed", this.recordRequestFailure], ["response", this.recordFailedResponse]]) {
        try {
          page.on(event, handler);
          if (event === "response" && !networkSources.includes("response_event")) networkSources.push("response_event");
        } catch {
          unavailableObservers.push(event);
        }
      }
    },
    evidence() {
      return { console_errors: consoleErrors, failed_requests: failedRequests, observed_network_sources: networkSources, unavailable_observers: unavailableObservers };
    },
  };
}

async function main() {
  if (!supportedNodeRuntime()) {
    throw new Error("Stagehand requires Node ^20.19.0 or >=22.12.0");
  }
  const { previewURL, reportURL, urlContext, output, plan: planPath } = await parseArguments(process.argv.slice(2));
  const env = environment();
  const model = modelConfiguration(env);
  const browserPlan = await loadBrowserPlan(planPath);
  const omitVisualScreenshots = requiresSensitiveScreenshotOmission(browserPlan);
  await fs.mkdir(path.dirname(output), { recursive: true, mode: 0o700 });
  let stagehand;
  const startedAt = new Date().toISOString();
  try {
    stagehand = new Stagehand({
      env,
      model,
      ...(env === "BROWSERBASE" ? { apiKey: process.env.BROWSERBASE_API_KEY.trim() } : {}),
      disablePino: true,
      logInferenceToFile: false,
	  logger: () => {},
    });
    await withTimeout(() => stagehand.init(), stagehandInitializationTimeoutMs, "Stagehand initialization");
    const page = stagehand.context.pages()[0];
    if (!page) throw new Error("Stagehand did not provide a browser page");
    const browserRuntime = boundedBrowserRuntime(urlContext);
    // Runtime evidence stores only fixed error codes and allowlisted signals;
    // raw browser messages and URLs can contain arbitrary page/account data.
    browserRuntime.attach(page);
    const desktop = responsiveViewport("desktop");
    await setVerifiedViewport(page, desktop);
    await navigatePreview(page, previewURL);
    await browserRuntime.capturePerformance(page);
    await waitForRenderedDocument(page);
    await waitForVisualStability(page);
    const browserE2E = await runBrowserCases(page, reportURL, browserPlan, path.dirname(output), browserRuntime, urlContext);
    const responsive = await runMobileResponsiveSmoke(page, previewURL, path.dirname(output), browserRuntime, urlContext, { suppressScreenshot: omitVisualScreenshots });
    const browserRuntimeEvidence = browserRuntime.evidence();
    let assessment;
    let extractionError = "";
    let providerResponseExcerpt = "";
    let providerUsage = null;
    let semanticCall = null;
    try {
      if (!model.isGateway) throw new Error("Stagehand requires the central inference gateway");
      const semantic = await assessWithGateway(page, model, {
        browser_e2e: browserE2E,
        responsive,
        browser_runtime: browserRuntimeEvidence,
      });
      assessment = semantic.assessment;
      providerUsage = semantic.usage;
      semanticCall = semantic.call;
    } catch (error) {
      // Keep diagnostics categorical; errors from browser/model libraries may
      // include snippets of user content and are not sent to the report.
      extractionError = "categorical_gateway_assessment_failed";
      providerUsage = error?.usage || error?.cause?.usage || null;
      semanticCall = error?.call || null;
      assessment = await fallbackAssessment(page);
    }
    const normalized = {
      title: "",
      primary_heading: "",
      primary_action: "",
      blocking_issue: PageAssessment.shape.blocking_issue.safeParse(assessment?.blocking_issue).success ? assessment.blocking_issue : "",
    };
    const hasApprovedBrowserCases = browserPlan.cases.length > 0;
    const semanticStatus = extractionError ? "degraded" : "structured";
    const browserRuntimePassedQA = browserRuntimePassed(browserRuntimeEvidence);
    const browserRuntimeObserved = browserRuntimeHasNetworkObservation(browserRuntimeEvidence);
    const verdict = !browserE2E.passed || !responsive.passed || !browserRuntimePassedQA
      ? "failed"
      : normalized.blocking_issue
        ? "failed"
        // A deterministic, human-approved E2E plan is stronger than a model's
        // formatting preference. The separate human QA gate remains mandatory,
        // and the semantic degradation is preserved as review evidence.
        : extractionError && !hasApprovedBrowserCases
          ? "blocked"
          : "passed";
    const stagehandUsage = safeMetrics(await stagehand.metrics);
    const usage = model.isGateway
      ? combinedUsage(stagehandUsage, providerUsage)
      : resolvedUsage(stagehandUsage, providerUsage);
    // This array is the immutable per-inference contract consumed by the
    // Delivery worker. Browser assertions and screenshots do not create
    // invented model rows because they have no provider usage.
    const calls = semanticCall ? [semanticCall] : extractionError ? [] : [{
      call_key: "semantic-assessment",
      call_status: "completed",
      provider: "gateway",
      model: "gateway-managed",
      usage: providerUsage,
      request: { instruction: "Categorical browser-signal assessment" },
      response: { status: "structured" },
    }];
    const effectiveProvider = semanticCall?.provider || model.provider;
    const effectiveModel = semanticCall?.model || model.ledgerModel;
    const caseScreenshots = [];
    const evidenceArtifacts = [];
    const report = {
      schema_version: 1,
      tool: "stagehand",
      mode: env.toLowerCase(),
      provider: effectiveProvider,
      model: effectiveModel,
      preview_url: reportURL,
      started_at: startedAt,
      completed_at: new Date().toISOString(),
      request: {
        instruction: "Run only the approved browser QA cases. AI receives categorical layout/runtime signals only; rendered text and screenshots are never sent. Remain read-only unless the reviewed plan explicitly uses approved_test_flow; then use only its ITBEM_QA_* test values, approved same-origin actions and immediate assertions. Never navigate outside the preview origin or expand the plan.",
		url: reportURL,
		mode: browserPlan.mode,
		browser_cases: browserPlan.cases.map((testCase, caseIndex) => ({
          id: `case-${caseIndex + 1}`,
          steps: testCase.steps.map((step, stepIndex) => ({ id: `step-${stepIndex + 1}`, kind: step.kind })),
        })),
	  },
      verdict,
      summary: !browserE2E.passed || !responsive.passed ? "An approved browser step or the mobile responsive smoke failed; human review is required." : !browserRuntimeObserved ? "The browser did not expose a trustworthy network-observation path; human review is required." : !browserRuntimePassedQA ? "The browser reported console errors or failed requests; human review is required." : normalized.blocking_issue ? `Potential blocking issue: ${normalized.blocking_issue}` : extractionError ? "Deterministic browser checks passed; categorical gateway assessment was unavailable and human review is required." : "Categorical browser-signal assessment completed without a reported blocking issue; human review remains mandatory.",
      assessment: normalized,
	  extraction: { status: extractionError ? "schema_rejected" : "structured", semantic_status: semanticStatus, strategy: model.isGateway ? "gateway_chat_json" : "stagehand_schema", error: extractionError, provider_response_excerpt: providerResponseExcerpt },
	  calls,
      browser_e2e: browserE2E,
      responsive,
      browser_runtime: browserRuntimeEvidence,
      evidence: {
        screenshot: "",
        case_screenshots: caseScreenshots,
        responsive_screenshot: responsive.screenshot,
        artifacts: evidenceArtifacts,
      },
      usage,
    };
    const sanitizedReport = sanitizeReportValue(report, urlContext);
    await fs.writeFile(output, `${JSON.stringify(sanitizedReport, null, 2)}\n`, { encoding: "utf8", mode: 0o600 });
    if (sanitizedReport.verdict !== "passed") process.exitCode = 1;
  } finally {
    if (stagehand) {
      try {
        await withTimeout(() => stagehand.close(), stagehandCloseTimeoutMs, "Stagehand shutdown");
      } catch {
        // Closing must never turn a completed report into a false pass. The
        // process still exits with this generic diagnostic after the report.
        if (!process.exitCode) process.exitCode = 2;
        process.stderr.write("Stagehand shutdown failed\n");
      }
    }
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(() => fail("Stagehand semantic QA failed; details withheld to protect page content."));
}
