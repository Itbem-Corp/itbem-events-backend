import fs from "node:fs/promises";
import { createHash } from "node:crypto";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { Stagehand } from "@browserbasehq/stagehand";
import { z } from "zod";

const maxURLLength = 2048;
const maxSummaryLength = 1200;
const minMeaningfulScreenshotBytes = 4096;
const maxBrowserQACases = 3;
const desktopViewport = Object.freeze({ name: "desktop", width: 1440, height: 1200 });
const mobileViewport = Object.freeze({ name: "mobile", width: 412, height: 915 });
// A browser QA job must be bounded even when a provider, a browser launch or
// an underlying transport becomes unhealthy. These are deliberately local
// runner limits: the worker still owns the broader task lease and retry policy.
const stagehandInitializationTimeoutMs = 35_000;
const miniMaxRequestTimeoutMs = 30_000;
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

function parseArguments(argv) {
  const values = new Map();
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    const value = argv[index + 1];
    if ((key !== "--url" && key !== "--output" && key !== "--plan") || !value || values.has(key)) {
      throw new Error("usage: node run.mjs --url <http(s) preview> --output <report.json> [--plan <browser-plan.json>]");
    }
    values.set(key, value);
  }
  if (!values.has("--url") || !values.has("--output")) {
    throw new Error("usage: node run.mjs --url <http(s) preview> --output <report.json> [--plan <browser-plan.json>]");
  }
  const previewURL = new URL(values.get("--url"));
  if (!/^https?:$/.test(previewURL.protocol) || previewURL.username || previewURL.password || previewURL.href.length > maxURLLength) {
    throw new Error("preview URL must be a bounded HTTP(S) URL without credentials");
  }
  const output = path.resolve(values.get("--output"));
  if (path.extname(output).toLowerCase() !== ".json") {
    throw new Error("semantic QA output must be a .json artifact");
  }
  const plan = values.has("--plan") ? path.resolve(values.get("--plan")) : "";
  if (plan && path.extname(plan).toLowerCase() !== ".json") {
    throw new Error("browser QA plan must be a .json file");
  }
  return { previewURL: previewURL.href, output, plan };
}

function environment() {
  const value = (process.env.STAGEHAND_QA_ENV ?? "LOCAL").trim().toUpperCase();
  if (value !== "LOCAL" && value !== "BROWSERBASE") {
    throw new Error("STAGEHAND_QA_ENV must be LOCAL or BROWSERBASE");
  }
  return value;
}

export function modelConfiguration(env) {
  const ledgerModel = (process.env.STAGEHAND_QA_MODEL ?? process.env.MINIMAX_MODEL ?? "").trim();
  const apiKey = (process.env.STAGEHAND_QA_API_KEY ?? process.env.MINIMAX_API_KEY ?? "").trim();
  const baseURL = (process.env.STAGEHAND_QA_BASE_URL ?? "https://api.minimax.io/v1").trim();
  if (!ledgerModel || !apiKey) {
    throw new Error("Stagehand requires STAGEHAND_QA_MODEL and STAGEHAND_QA_API_KEY (or the already configured MINIMAX_API_KEY)");
  }
  if (env === "BROWSERBASE" && !(process.env.BROWSERBASE_API_KEY ?? "").trim()) {
    throw new Error("BROWSERBASE_API_KEY is required when STAGEHAND_QA_ENV=BROWSERBASE");
  }
  // Keep the original MiniMax model identity separately for ITBEM's cost
  // ledger while Stagehand uses the OpenAI-compatible model convention.
  const modelName = ledgerModel.includes("/") ? ledgerModel : `openai/${ledgerModel}`;
  let endpoint;
  try {
    endpoint = new URL(baseURL);
  } catch {
    throw new Error("Stagehand QA base URL must be a valid HTTPS MiniMax endpoint");
  }
  // The Delivery worker supplies MINIMAX_API_KEY only to this pinned runner.
  // Treat the endpoint as a credential boundary, not as a convenient model
  // setting: an accidental proxy, HTTP URL or look-alike host must never
  // receive that credential. Supporting a different provider later requires
  // its own explicit credential source and reviewed runner contract.
  if (endpoint.protocol !== "https:" || endpoint.hostname.toLowerCase() !== "api.minimax.io" || endpoint.port || endpoint.username || endpoint.password || endpoint.search || endpoint.hash || !/^\/v1\/?$/.test(endpoint.pathname)) {
    throw new Error("Stagehand QA requires the canonical HTTPS MiniMax v1 endpoint");
  }
  const provider = "minimax";
  // MiniMax exposes the OpenAI-compatible Chat Completions shape; selecting
  // it explicitly avoids Stagehand's Responses-API default.
  return { modelName, ledgerModel, apiKey, baseURL: endpoint.href.replace(/\/$/, ""), provider, isMiniMax: true, openaiEndpointFormat: "chat" };
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

// The manifest travels inside the immutable JSON report while the PNG files
// travel as separate artifacts. SHA-256 lets the dashboard/S3 viewer prove
// that the displayed visual evidence is the exact file Stagehand captured.
async function screenshotEvidence(directory, name, metadata) {
  const body = await fs.readFile(path.join(directory, name));
  return {
    name,
    content_type: "image/png",
    bytes: body.byteLength,
    sha256: createHash("sha256").update(body).digest("hex"),
    captured_at: metadata.capturedAt,
    url: safeText(metadata.url, maxURLLength),
    viewport: metadata.viewport,
  };
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
  const numeric = (name) => Number.isFinite(source[name]) ? Math.max(0, Math.trunc(source[name])) : 0;
  const input = numeric("inputTokens");
  const output = numeric("outputTokens");
  const reasoning = numeric("reasoningTokens");
  const cached = numeric("cachedInputTokens");
  const cacheWrite = numeric("cacheCreationInputTokens") || numeric("cacheWriteInputTokens");
  return {
    input_tokens: input,
    output_tokens: output,
    reasoning_tokens: reasoning,
    cached_input_tokens: cached,
    cache_write_tokens: cacheWrite,
    // Provider total_tokens normally already includes reasoning within output.
    // Fall back to the two billable aggregate dimensions, never their sum
    // plus reasoning, so cost and displayed total remain consistent.
    total_tokens: Math.max(numeric("totalTokens"), input + output),
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
  title: z.string().max(280).optional(),
  primary_heading: z.string().max(280).optional(),
  primary_action: z.string().max(280).optional(),
  blocking_issue: z.string().max(600).optional(),
});

const minMaxSemanticSystemPrompt = [
  "You are a careful read-only web QA reviewer.",
  "Use only the supplied browser-derived evidence.",
  "Return one JSON object and nothing else.",
  "It must have exactly these string keys: title, primary_heading, primary_action, blocking_issue.",
  "Use an empty string when a value is not evidenced or no clearly blocking issue is visible.",
  "Never infer credentials, private data, hidden state, or interactions that were not supplied.",
].join(" ");

function approvedTestValues() {
  return Object.entries(process.env)
    .filter(([name, value]) => /^ITBEM_QA_[A-Z0-9_]{1,60}$/.test(name) && typeof value === "string" && value.length >= 3)
    .map(([, value]) => value)
    .sort((left, right) => right.length - left.length);
}

export function redactedInferenceText(value, limit = 6000) {
  let redacted = boundedPrivateText(value, limit)
    .replace(/\b(?:sk|pk|rk|AKIA)[-_a-zA-Z0-9]{16,}\b/g, "[REDACTED_SECRET]")
    .replace(/\b(?:bearer|token|api[_ -]?key|password|secret)\s*[:=]\s*\S+/gi, "$1=[REDACTED]")
    .replace(/data:[^\s]{1,4096}/gi, "[REDACTED_DATA_URL]");
  for (const testValue of approvedTestValues()) redacted = redacted.split(testValue).join("[REDACTED_TEST_VALUE]");
  return redacted;
}

// Browser evidence is structured and bounded before it reaches MiniMax. Test
// values must be available to Stagehand for a human-approved E2E flow, but a
// page is allowed to reflect them; never let that reflection cross the model
// boundary. The private local report remains the audited record of the run.
export function redactedInferenceEvidence(value, depth = 0) {
  if (depth > 8) return "[TRUNCATED_EVIDENCE]";
  if (typeof value === "string") return redactedInferenceText(value, 6000);
  if (typeof value === "number" || typeof value === "boolean" || value === null) return value;
  if (Array.isArray(value)) return value.slice(0, 48).map((entry) => redactedInferenceEvidence(entry, depth + 1));
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).slice(0, 80).map(([key, entry]) => [key, redactedInferenceEvidence(entry, depth + 1)]));
  }
  return "";
}

function parseMiniMaxAssessment(content) {
  const text = boundedPrivateText(content, 10_000)
    .replace(/<think>[\s\S]*?<\/think>/gi, "")
    .replace(/^\s*```(?:json)?\s*/i, "")
    .replace(/\s*```\s*$/i, "")
    .trim();
  const start = text.indexOf("{");
  const end = text.lastIndexOf("}");
  if (start < 0 || end <= start) throw new Error("MiniMax did not return a JSON object");
  const parsed = JSON.parse(text.slice(start, end + 1));
  const validated = PageAssessment.safeParse(parsed);
  if (!validated.success) throw new Error("MiniMax JSON did not match the QA assessment contract");
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

// The private report preserves exactly what ITBEM sent and the provider's
// answer that is useful for review. It intentionally excludes reasoning
// traces: they are neither required to reproduce the request nor appropriate
// evidence for a human delivery gate.
function miniMaxResponseAudit(payload) {
  const choices = Array.isArray(payload?.choices) ? payload.choices.map((choice, index) => ({
    index: Number.isInteger(choice?.index) ? choice.index : index,
    finish_reason: safeText(choice?.finish_reason, 80),
    message: {
      role: safeText(choice?.message?.role, 40),
      content: boundedPrivateText(choice?.message?.content, 16_000),
    },
  })) : [];
  return {
    id: safeText(payload?.id, 180),
    object: safeText(payload?.object, 80),
    created: Number.isFinite(payload?.created) ? Math.trunc(payload.created) : undefined,
    model: safeText(payload?.model, 180),
    choices,
    usage: payload?.usage && typeof payload.usage === "object" ? payload.usage : {},
  };
}

async function browserSemanticEvidence(page, executionEvidence = {}) {
  const fallback = await fallbackAssessment(page);
  try {
    const bodyText = await page.locator("body").innerText();
    return {
      ...fallback,
      visible_text: redactedInferenceText(bodyText, 6000),
      // MiniMax is not being asked to guess whether a UI works from a page
      // snapshot alone. Give it the actual, already-completed browser
      // contract and runtime signals so its review is grounded in what
      // Stagehand just exercised.
      approved_browser_e2e: redactedInferenceEvidence(executionEvidence.browser_e2e ?? {}),
      responsive: redactedInferenceEvidence(executionEvidence.responsive ?? {}),
      browser_runtime: redactedInferenceEvidence(executionEvidence.browser_runtime ?? {}),
    };
  } catch {
    return {
      ...fallback,
      visible_text: "",
      approved_browser_e2e: redactedInferenceEvidence(executionEvidence.browser_e2e ?? {}),
      responsive: redactedInferenceEvidence(executionEvidence.responsive ?? {}),
      browser_runtime: redactedInferenceEvidence(executionEvidence.browser_runtime ?? {}),
    };
  }
}

async function assessWithMiniMax(page, model, executionEvidence) {
  const evidence = await browserSemanticEvidence(page, executionEvidence);
  const endpoint = `${model.baseURL.replace(/\/+$/, "")}/chat/completions`;
  const requestPayload = {
    model: model.ledgerModel,
    messages: [
      { role: "system", content: minMaxSemanticSystemPrompt },
      { role: "user", content: `Browser QA evidence (read-only):\n${JSON.stringify(evidence)}` },
    ],
    // MiniMax documents 1.0 as the supported/recommended temperature for
    // its OpenAI-compatible M-series API.
    temperature: 1,
    reasoning_split: true,
  };
  const requestAudit = { endpoint, body: requestPayload };
  let response;
  const controller = new AbortController();
  const requestTimeout = setTimeout(() => controller.abort(new Error(`MiniMax request timed out after ${miniMaxRequestTimeoutMs}ms`)), miniMaxRequestTimeoutMs);
  try {
    response = await fetch(endpoint, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${model.apiKey}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(requestPayload),
      signal: controller.signal,
    });
  } catch (error) {
    throw Object.assign(error, {
      usage: safeProviderUsage({}),
      call: { call_key: "semantic-assessment", call_status: "failed", provider: model.provider, model: model.ledgerModel, usage: safeProviderUsage({}), request: requestAudit, response: { transport_error: safeText(error?.message, 600) } },
    });
  } finally {
    clearTimeout(requestTimeout);
  }
  const payload = await response.json().catch(() => ({}));
  const usage = miniMaxUsage(payload, response.status);
  const call = { call_key: "semantic-assessment", call_status: "completed", provider: model.provider, model: model.ledgerModel, usage, request: requestAudit, response: miniMaxResponseAudit(payload) };
  if (!response.ok) {
    call.call_status = "failed";
    throw Object.assign(new Error(safeText(payload?.error?.message || payload?.base_resp?.status_msg || `MiniMax request failed (${response.status})`, 600)), { usage, call });
  }
  const content = payload?.choices?.[0]?.message?.content;
  try {
    return { assessment: parseMiniMaxAssessment(content), usage, responseExcerpt: "", call };
  } catch (error) {
    call.call_status = "failed";
    throw Object.assign(error, { usage, call, responseExcerpt: boundedPrivateText(content, 16_000) });
  }
}

async function fallbackAssessment(page) {
  const title = safeText(await page.title(), 280);
  try {
    const visible = await page.evaluate(() => {
      const text = (element) => (element?.textContent || "").replace(/\s+/g, " ").trim();
      return {
        primary_heading: text(document.querySelector("h1, [role='heading'][aria-level='1']")),
        primary_action: text(document.querySelector("button, [role='button'], a[href]")),
      };
    });
    return { title, primary_heading: safeText(visible?.primary_heading, 280), primary_action: safeText(visible?.primary_action, 280) };
  } catch {
    return { title, primary_heading: "", primary_action: "" };
  }
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
  const caseIDs = new Set();
  const cases = parsed.cases.map((testCase) => {
    if (!testCase || typeof testCase !== "object") throw new Error("browser QA case is invalid");
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
    return { id, title, steps };
  });
  return { schema_version: 1, mode: parsed.mode, cases };
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

async function runBrowserCases(page, previewURL, plan, directory, browserRuntime) {
  const cases = [];
  let passed = true;
  for (let caseIndex = 0; caseIndex < plan.cases.length; caseIndex += 1) {
    const testCase = plan.cases[caseIndex];
    const steps = [];
    const beforeScreenshot = await captureCaseScreenshot(page, directory, `semantic-qa-case-${String(caseIndex + 1).padStart(2, "0")}-before.png`);
    if (!beforeScreenshot.name) passed = false;
    for (const step of testCase.steps) {
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
      } catch (error) {
        stepPassed = false;
        detail = safeText(error?.message || "Browser QA step failed", 400);
      }
      await browserRuntime?.capturePerformance(page);
      if (!stepPassed) passed = false;
      steps.push({ id: step.id, kind: step.kind, passed: stepPassed, detail, url: safeText(page.url(), 1024), usage: { input_tokens: 0, output_tokens: 0, cached_input_tokens: 0, reasoning_tokens: 0, total_tokens: 0 } });
      if (!stepPassed) break;
    }
    const afterScreenshot = await captureCaseScreenshot(page, directory, `semantic-qa-case-${String(caseIndex + 1).padStart(2, "0")}-after.png`);
    if (!afterScreenshot.name) passed = false;
    const casePassed = steps.length === testCase.steps.length && steps.every((step) => step.passed) && Boolean(beforeScreenshot.name) && Boolean(afterScreenshot.name);
    cases.push({
      id: testCase.id,
      title: testCase.title,
      passed: casePassed,
      steps,
      // Keep `screenshot` as the after state for older dashboard versions,
      // while the named pair gives the QA gate true before/after evidence.
      screenshot: afterScreenshot.name,
      screenshot_captured_at: afterScreenshot.captured_at,
      screenshot_url: afterScreenshot.url,
      screenshot_viewport: afterScreenshot.viewport,
      before_screenshot: beforeScreenshot.name,
      before_screenshot_captured_at: beforeScreenshot.captured_at,
      before_screenshot_url: beforeScreenshot.url,
      before_screenshot_viewport: beforeScreenshot.viewport,
      evidence_error: beforeScreenshot.error || afterScreenshot.error,
    });
    // Stop after a failed case. Continuing into later navigation or approved
    // clicks from an unknown browser state would make the evidence ambiguous.
    if (!casePassed) break;
  }
  return { mode: plan.mode, passed, cases };
}

async function captureCaseScreenshot(page, directory, name) {
  const capturedAt = new Date().toISOString();
  const url = safeText(page.url(), maxURLLength);
  const viewport = await browserViewport(page);
  try {
    await fs.writeFile(path.join(directory, name), await page.screenshot({ fullPage: true }), { mode: 0o600 });
    return { name, captured_at: capturedAt, url, viewport, error: "" };
  } catch (error) {
    return { name: "", captured_at: capturedAt, url, viewport, error: safeText(error?.message || "Case screenshot could not be captured", 400) };
  }
}

async function waitForRenderedDocument(page) {
  // `domcontentloaded` says the document exists, not that Chromium has painted
  // it at the new emulated viewport. Wait for a visible body and two frames so
  // visual evidence is a real render rather than a race-prone empty bitmap.
  const rendered = await page.evaluate(async () => {
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    const body = document.body;
    const style = body ? getComputedStyle(body) : null;
    return Boolean(body && style && style.visibility !== "hidden" && style.display !== "none" && (body.innerText || body.querySelector("img, svg, canvas, [role], input, button")));
  });
  if (!rendered) throw new Error("Mobile document did not render visible page content");
}

// Responsive QA is deterministic and happens in addition to the reviewed
// browser cases: it never clicks or mutates state. The separate mobile image
// and overflow measurement give the human QA gate useful evidence even when
// a plan's feature-specific assertions only apply to the desktop layout.
async function runMobileResponsiveSmoke(page, previewURL, directory, browserRuntime) {
  const viewport = responsiveViewport("mobile");
  const screenshotName = "semantic-qa-preview-mobile.png";
  const startedAt = new Date().toISOString();
  let passed = true;
  let detail = "";
  let url = previewURL;
  let overflow = null;
  try {
    await setVerifiedViewport(page, viewport);
    await page.goto(previewURL, { waitUntil: "domcontentloaded", timeoutMs: 45_000 });
    await waitForRenderedDocument(page);
    url = safeText(page.url(), maxURLLength);
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
    detail = safeText(error?.message || "Mobile responsive smoke failed", 400);
  }
  let screenshot = "";
  try {
    // A viewport image is deliberate here. Some Chromium/CDP combinations can
    // produce an empty full-page bitmap immediately after a viewport emulation
    // change even though the DOM is available. The QA gate needs the rendered
    // mobile interface above the fold, not a misleading white artifact.
    let image = null;
    for (let attempt = 0; attempt < 3; attempt += 1) {
      await waitForRenderedDocument(page);
      const candidate = await page.screenshot({ fullPage: false, animations: "disabled", scale: "css" });
      if (candidate.byteLength >= minMeaningfulScreenshotBytes) {
        image = candidate;
        break;
      }
    }
    if (!image) throw new Error("Mobile screenshot did not contain enough rendered visual evidence");
    await fs.writeFile(path.join(directory, screenshotName), image, { mode: 0o600 });
    screenshot = screenshotName;
  } catch (error) {
    passed = false;
    if (!detail) detail = safeText(error?.message || "Mobile screenshot could not be captured", 400);
  }
  return {
    viewport,
    passed,
    detail,
    url,
    overflow,
    screenshot,
    captured_at: new Date().toISOString(),
    started_at: startedAt,
  };
}

export function boundedBrowserRuntime() {
  const consoleErrors = [];
  const failedRequests = [];
  const unavailableObservers = [];
  const networkSources = [];
  const record = (entries, value, limit = 320) => {
    const normalized = redactedInferenceText(value, limit);
    if (normalized && entries.length < 12) entries.push(normalized);
  };
  return {
    recordConsole(message) {
      if (message?.type?.() === "error") record(consoleErrors, message.text?.());
    },
    recordPageError(error) {
      record(consoleErrors, error?.message || error);
    },
    recordRequestFailure(request) {
      const failure = request?.failure?.();
      if (failure?.errorText) record(failedRequests, `${request.method?.() || "REQUEST"} ${request.url?.() || ""}: ${failure.errorText}`);
    },
    recordFailedResponse(response) {
      const status = Number(response?.status?.());
      if (!Number.isFinite(status) || status < 400) return;
      const request = response?.request?.();
      record(failedRequests, `${request?.method?.() || "REQUEST"} ${request?.url?.() || response?.url?.() || ""}: HTTP ${Math.trunc(status)}`);
    },
    async capturePerformance(page) {
      try {
        const entries = await page.evaluate(() => {
          const performanceEntries = [
            ...performance.getEntriesByType("navigation"),
            ...performance.getEntriesByType("resource"),
          ];
          return performanceEntries.slice(-200).map((entry) => ({
            name: String(entry.name || ""),
            initiator_type: String(entry.initiatorType || ""),
            response_status: Number(entry.responseStatus || 0),
          }));
        });
        if (!networkSources.includes("performance_timing")) networkSources.push("performance_timing");
        for (const entry of Array.isArray(entries) ? entries : []) {
          const status = Number(entry?.response_status);
          if (Number.isFinite(status) && status >= 400) {
            record(failedRequests, `${safeText(entry?.initiator_type || "REQUEST", 40).toUpperCase()} ${safeText(entry?.name, 1024)}: HTTP ${Math.trunc(status)}`);
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
  const { previewURL, output, plan: planPath } = parseArguments(process.argv.slice(2));
  const env = environment();
  const model = modelConfiguration(env);
  const browserPlan = await loadBrowserPlan(planPath);
  await fs.mkdir(path.dirname(output), { recursive: true, mode: 0o700 });
  const screenshot = path.join(path.dirname(output), `${path.basename(output, ".json")}.png`);
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
    const browserRuntime = boundedBrowserRuntime();
    // Runtime errors are evidence, not a model-created diagnosis. Preserve a
    // small redacted record for MiniMax and the human QA gate.
    browserRuntime.attach(page);
    const desktop = responsiveViewport("desktop");
    await setVerifiedViewport(page, desktop);
    await page.goto(previewURL, { waitUntil: "domcontentloaded", timeoutMs: 45_000 });
    await browserRuntime.capturePerformance(page);
    await fs.writeFile(screenshot, await page.screenshot({ fullPage: true }), { mode: 0o600 });
    const landingScreenshotCapturedAt = new Date().toISOString();
    const landingScreenshotURL = safeText(page.url(), maxURLLength);
    const landingViewport = await browserViewport(page);
    const browserE2E = await runBrowserCases(page, previewURL, browserPlan, path.dirname(output), browserRuntime);
    const responsive = await runMobileResponsiveSmoke(page, previewURL, path.dirname(output), browserRuntime);
    const browserRuntimeEvidence = browserRuntime.evidence();
    let assessment;
    let extractionError = "";
    let providerResponseExcerpt = "";
    let providerUsage = null;
    let semanticCall = null;
    try {
      if (model.isMiniMax) {
        const semantic = await assessWithMiniMax(page, model, {
          browser_e2e: browserE2E,
          responsive,
          browser_runtime: browserRuntimeEvidence,
        });
        assessment = semantic.assessment;
        providerUsage = semantic.usage;
        semanticCall = semantic.call;
      } else {
        assessment = await stagehand.extract(
          "Return only the requested structured fields. Inspect this preview read-only. Identify its visible page title, primary heading, primary action and one clearly blocking usability issue if present. Do not click, submit, authenticate, mutate data or navigate away.",
          PageAssessment,
        );
      }
    } catch (error) {
      extractionError = safeText(error?.message || "Stagehand did not produce a structured assessment", 600);
      providerResponseExcerpt = boundedPrivateText(error?.responseExcerpt || error?.text || error?.cause?.text || "");
      providerUsage = error?.usage || error?.cause?.usage || null;
      semanticCall = error?.call || null;
      assessment = await fallbackAssessment(page);
    }
    const normalized = {
      title: safeText(assessment?.title, 280),
      primary_heading: safeText(assessment?.primary_heading, 280),
      primary_action: safeText(assessment?.primary_action, 280),
      blocking_issue: safeText(assessment?.blocking_issue, 600),
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
    const usage = model.isMiniMax
      ? combinedUsage(stagehandUsage, providerUsage)
      : resolvedUsage(stagehandUsage, providerUsage);
    // This array is the immutable per-inference contract consumed by the
    // Delivery worker. Browser assertions and screenshots do not create
    // invented model rows because they have no provider usage.
    const calls = semanticCall ? [semanticCall] : extractionError ? [] : [{
      call_key: "semantic-assessment",
      call_status: "completed",
      provider: model.provider,
      model: model.ledgerModel,
      usage: model.isMiniMax ? providerUsage : usage,
      request: { instruction: "Stagehand schema extraction" },
      response: { status: "structured" },
    }];
    const caseScreenshots = browserE2E.cases.flatMap((testCase) => [testCase.before_screenshot, testCase.screenshot]).filter(Boolean);
    const evidenceArtifacts = await Promise.all([
      screenshotEvidence(path.dirname(output), path.basename(screenshot), {
        capturedAt: landingScreenshotCapturedAt,
        url: landingScreenshotURL,
        viewport: landingViewport,
      }),
      ...browserE2E.cases.flatMap((testCase) => [
        ...(testCase.before_screenshot ? [screenshotEvidence(path.dirname(output), testCase.before_screenshot, {
          capturedAt: testCase.before_screenshot_captured_at,
          url: testCase.before_screenshot_url,
          viewport: testCase.before_screenshot_viewport,
        })] : []),
        ...(testCase.screenshot ? [screenshotEvidence(path.dirname(output), testCase.screenshot, {
          capturedAt: testCase.screenshot_captured_at,
          url: testCase.screenshot_url,
          viewport: testCase.screenshot_viewport,
        })] : []),
      ]),
      ...(responsive.screenshot ? [screenshotEvidence(path.dirname(output), responsive.screenshot, {
        capturedAt: responsive.captured_at,
        url: responsive.url,
        viewport: responsive.viewport,
      })] : []),
    ]);
    const report = {
      schema_version: 1,
      tool: "stagehand",
      mode: env.toLowerCase(),
      provider: model.provider,
      model: model.ledgerModel,
      preview_url: previewURL,
      started_at: startedAt,
      completed_at: new Date().toISOString(),
      request: {
		instruction: "Run only the approved browser QA cases, then inspect the resulting preview. Remain read-only unless the reviewed plan explicitly uses approved_test_flow; then use only its ITBEM_QA_* test values, approved same-origin actions and immediate assertions. Never navigate outside the preview origin or expand the plan.",
		url: previewURL,
		mode: browserPlan.mode,
		browser_cases: browserPlan.cases,
	  },
      verdict,
      summary: !browserE2E.passed || !responsive.passed ? "An approved browser step or the mobile responsive smoke failed; human review is required." : !browserRuntimeObserved ? "The browser did not expose a trustworthy network-observation path; human review is required." : !browserRuntimePassedQA ? "The browser reported console errors or failed requests; human review is required." : normalized.blocking_issue ? `Potential blocking issue: ${normalized.blocking_issue}` : extractionError ? "Approved deterministic browser QA and mobile responsive smoke passed; the semantic model response was retained as degraded private evidence for the mandatory human QA review." : "Approved browser QA, mobile responsive smoke and semantic visual review completed without a reported blocking issue.",
      assessment: normalized,
	  extraction: { status: extractionError ? "schema_rejected" : "structured", semantic_status: semanticStatus, strategy: model.isMiniMax ? "minimax_chat_json" : "stagehand_schema", error: extractionError, provider_response_excerpt: providerResponseExcerpt },
	  calls,
      browser_e2e: browserE2E,
      responsive,
      browser_runtime: browserRuntimeEvidence,
      evidence: {
        screenshot: path.basename(screenshot),
        case_screenshots: caseScreenshots,
        responsive_screenshot: responsive.screenshot,
        artifacts: evidenceArtifacts,
      },
      usage,
    };
    await fs.writeFile(output, `${JSON.stringify(report, null, 2)}\n`, { encoding: "utf8", mode: 0o600 });
    if (report.verdict !== "passed") process.exitCode = 1;
  } finally {
    if (stagehand) {
      try {
        await withTimeout(() => stagehand.close(), stagehandCloseTimeoutMs, "Stagehand shutdown");
      } catch (error) {
        // Closing must never turn a completed report into a false pass. The
        // process will still exit with this error after the report is written.
        if (!process.exitCode) process.exitCode = 2;
        process.stderr.write(`${safeText(error?.message || "Stagehand shutdown failed", 600)}\n`);
      }
    }
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => fail(safeText(error?.message || "Stagehand semantic QA failed", 600)));
}
