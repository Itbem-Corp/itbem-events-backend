import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { boundedBrowserRuntime, browserRuntimeHasNetworkObservation, browserRuntimePassed, combinedUsage, loadBrowserPlan, miniMaxUsage, modelConfiguration, redactedInferenceEvidence, redactedInferenceText, resolvedUsage, responsiveViewport, safeMetrics, safeProviderUsage, withTimeout } from "./run.mjs";

test("responsive QA uses bounded reviewable desktop and mobile viewports", () => {
  assert.deepEqual(responsiveViewport("desktop"), { name: "desktop", width: 1440, height: 1200 });
  assert.deepEqual(responsiveViewport("mobile"), { name: "mobile", width: 412, height: 915 });
  assert.throws(() => responsiveViewport("tablet"), /unsupported responsive QA viewport/);
});

test("Stagehand metrics keep reasoning separate without inflating total tokens", () => {
  const usage = safeMetrics({
    totalPromptTokens: 120,
    totalCompletionTokens: 80,
    totalReasoningTokens: 30,
    totalCachedInputTokens: 40,
    totalCacheCreationInputTokens: 12,
  });
  assert.deepEqual(usage, {
    input_tokens: 120,
    output_tokens: 80,
    reasoning_tokens: 30,
    cached_input_tokens: 40,
    cache_write_tokens: 12,
    total_tokens: 200,
    inference_ms: 0,
  });
});

test("provider usage preserves cache-write detail and respects reported aggregate", () => {
  const usage = safeProviderUsage({
    inputTokens: 90,
    outputTokens: 70,
    reasoningTokens: 25,
    cachedInputTokens: 20,
    cacheWriteInputTokens: 10,
    totalTokens: 160,
  });
  assert.deepEqual(usage, {
    input_tokens: 90,
    output_tokens: 70,
    reasoning_tokens: 25,
    cached_input_tokens: 20,
    cache_write_tokens: 10,
    total_tokens: 160,
    inference_ms: 0,
  });
});

test("MiniMax semantic usage preserves a bounded provider outcome for the ledger", () => {
  const usage = miniMaxUsage({
    choices: [{ finish_reason: "stop" }],
    usage: { prompt_tokens: 12, completion_tokens: 4, total_tokens: 16, input_sensitive: true },
    base_resp: { status_code: 0 },
  }, 200);
  assert.deepEqual(usage, {
    input_tokens: 12,
    output_tokens: 4,
    reasoning_tokens: 0,
    cached_input_tokens: 0,
    cache_write_tokens: 0,
    total_tokens: 16,
    inference_ms: 0,
    _itbem_provider: { finish_reason: "stop", input_sensitive: true, status_code: 200 },
  });
});

test("Stagehand keeps the MiniMax credential behind its canonical HTTPS endpoint", (t) => {
  const names = ["STAGEHAND_QA_MODEL", "MINIMAX_MODEL", "STAGEHAND_QA_API_KEY", "MINIMAX_API_KEY", "STAGEHAND_QA_BASE_URL"];
  const previous = Object.fromEntries(names.map((name) => [name, process.env[name]]));
  t.after(() => {
    for (const name of names) {
      if (previous[name] === undefined) delete process.env[name]; else process.env[name] = previous[name];
    }
  });
  process.env.STAGEHAND_QA_MODEL = "MiniMax-M3";
  process.env.MINIMAX_API_KEY = "test-key-not-a-real-secret";
  process.env.STAGEHAND_QA_BASE_URL = "https://api.minimax.io/v1/";
  const configured = modelConfiguration("LOCAL");
  assert.equal(configured.provider, "minimax");
  assert.equal(configured.isMiniMax, true);
  assert.equal(configured.baseURL, "https://api.minimax.io/v1");

  process.env.STAGEHAND_QA_BASE_URL = "http://api.minimax.io/v1";
  assert.throws(() => modelConfiguration("LOCAL"), /canonical HTTPS MiniMax v1 endpoint/);
  process.env.STAGEHAND_QA_BASE_URL = "https://api.minimax.io.attacker.invalid/v1";
  assert.throws(() => modelConfiguration("LOCAL"), /canonical HTTPS MiniMax v1 endpoint/);
  process.env.STAGEHAND_QA_BASE_URL = "https://api.minimax.io/v1?proxy=unexpected";
  assert.throws(() => modelConfiguration("LOCAL"), /canonical HTTPS MiniMax v1 endpoint/);
});

test("bounded runner operations fail instead of waiting indefinitely", async () => {
  await assert.rejects(
    withTimeout(() => new Promise(() => {}), 10, "provider call"),
    /provider call timed out after 10ms/,
  );
});

test("fallback provider usage fills missing Stagehand dimensions without changing total semantics", () => {
  const usage = resolvedUsage(
    { totalPromptTokens: 4, totalCompletionTokens: 6, totalReasoningTokens: 2 },
    { inputTokens: 8, outputTokens: 9, cachedInputTokens: 3, cacheCreationInputTokens: 1, totalTokens: 17 },
  );
  assert.deepEqual(usage, {
    input_tokens: 4,
    output_tokens: 6,
    reasoning_tokens: 2,
    cached_input_tokens: 3,
    cache_write_tokens: 1,
    total_tokens: 17,
    inference_ms: 0,
  });
});

test("separate browser and MiniMax calls are summed rather than overwritten", () => {
  const usage = combinedUsage(
    { input_tokens: 4, output_tokens: 6, reasoning_tokens: 2, cached_input_tokens: 0, cache_write_tokens: 0, total_tokens: 10, inference_ms: 5 },
    { input_tokens: 8, output_tokens: 9, reasoning_tokens: 3, cached_input_tokens: 1, cache_write_tokens: 0, total_tokens: 17, inference_ms: 0 },
  );
  assert.deepEqual(usage, {
    input_tokens: 12,
    output_tokens: 15,
    reasoning_tokens: 5,
    cached_input_tokens: 1,
    cache_write_tokens: 0,
    total_tokens: 27,
    inference_ms: 5,
  });
});

test("browser runtime errors and failed requests block a passing QA verdict", () => {
  const observed = { console_errors: [], failed_requests: [], observed_network_sources: ["performance_timing"], unavailable_observers: ["pageerror"] };
  assert.equal(browserRuntimePassed(observed), true);
  assert.equal(browserRuntimePassed({ ...observed, console_errors: ["TypeError: broken"] }), false);
  assert.equal(browserRuntimePassed({ ...observed, failed_requests: ["GET /api: net::ERR_FAILED"] }), false);
  assert.equal(browserRuntimeHasNetworkObservation({ console_errors: [], failed_requests: [] }), false);
  assert.equal(browserRuntimePassed({ console_errors: [], failed_requests: [] }), false);
});

test("HTTP error responses are evidence, even when the browser request technically completed", () => {
  const runtime = boundedBrowserRuntime();
  runtime.recordFailedResponse({
    status: () => 500,
    request: () => ({ method: () => "GET", url: () => "http://preview.local/api/health" }),
  });
  runtime.recordFailedResponse({ status: () => 200, request: () => ({ method: () => "GET", url: () => "http://preview.local/" }) });
  const evidence = runtime.evidence();
  assert.deepEqual(evidence.failed_requests, ["GET http://preview.local/api/health: HTTP 500"]);
  assert.equal(browserRuntimePassed(evidence), false);
});

test("performance timing preserves HTTP failures when Stagehand does not expose response events", async () => {
  const runtime = boundedBrowserRuntime();
  await runtime.capturePerformance({
    evaluate: async () => [{ name: "http://preview.local/api/tasks", initiator_type: "fetch", response_status: 503 }],
  });
  const evidence = runtime.evidence();
  assert.deepEqual(evidence.observed_network_sources, ["performance_timing"]);
  assert.deepEqual(evidence.failed_requests, ["FETCH http://preview.local/api/tasks: HTTP 503"]);
  assert.equal(browserRuntimePassed(evidence), false);
});

test("approved browser test values are redacted before browser evidence reaches MiniMax", (t) => {
  const key = "ITBEM_QA_REDACTION_TEST";
  const previous = process.env[key];
  process.env[key] = "qa-secret-value-123";
  t.after(() => {
    if (previous === undefined) delete process.env[key]; else process.env[key] = previous;
  });
  assert.equal(redactedInferenceText("Visible qa-secret-value-123 in browser"), "Visible [REDACTED_TEST_VALUE] in browser");
  assert.deepEqual(redactedInferenceEvidence({ url: "http://preview.local/?value=qa-secret-value-123", nested: ["qa-secret-value-123"] }), {
    url: "http://preview.local/?value=[REDACTED_TEST_VALUE]",
    nested: ["[REDACTED_TEST_VALUE]"],
  });
});

test("an approved test-flow click needs deterministic evidence immediately after it", async (t) => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "itbem-stagehand-plan-"));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const planPath = path.join(directory, "plan.json");
  const base = {
    schema_version: 1,
    mode: "approved_test_flow",
    cases: [{
      id: "login",
      title: "Isolated login",
      steps: [
        { kind: "navigate", path: "/login" },
        { kind: "fill", selector: "#email", value_env: "ITBEM_QA_LOGIN_EMAIL" },
        { kind: "fill", selector: "#password", value_env: "ITBEM_QA_LOGIN_PASSWORD" },
        { kind: "click", selector: "button[type=submit]" },
      ],
    }],
  };
  await fs.writeFile(planPath, JSON.stringify(base));
  await assert.rejects(loadBrowserPlan(planPath), /immediate post-action assertion/);

  base.cases[0].steps.push({ kind: "assert_path", path: "/" });
  await fs.writeFile(planPath, JSON.stringify(base));
  const parsed = await loadBrowserPlan(planPath);
  assert.equal(parsed.cases[0].steps.at(-1).kind, "assert_path");
});

test("browser evidence capacity rejects more than three reviewed cases", async (t) => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "itbem-stagehand-plan-"));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const planPath = path.join(directory, "plan.json");
  const cases = Array.from({ length: 4 }, (_, index) => ({
    id: `case-${index + 1}`,
    title: `Bounded case ${index + 1}`,
    steps: [{ kind: "navigate", path: "/login" }],
  }));
  await fs.writeFile(planPath, JSON.stringify({ schema_version: 1, mode: "read_only", cases }));
  await assert.rejects(loadBrowserPlan(planPath), /unsupported shape/);
});
