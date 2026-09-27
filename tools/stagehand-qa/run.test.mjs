import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { assessWithGateway, boundedBrowserRuntime, browserRuntimeHasNetworkObservation, browserRuntimePassed, combinedUsage, createPreviewURLContext, loadBrowserPlan, miniMaxUsage, modelConfiguration, navigatePreview, parseArguments, redactedInferenceEvidence, redactedInferenceText, requiresSensitiveScreenshotOmission, resolvedUsage, responsiveViewport, runBrowserCases, runMobileResponsiveSmoke, safeMetrics, safeProviderUsage, sanitizeReportValue, sendGatewayInference, withTimeout } from "./run.mjs";

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

test("gateway provider-native usage is preserved from MiniMax and OpenAI response shapes", () => {
  assert.deepEqual(safeProviderUsage({
    prompt_tokens: 90,
    completion_tokens: 70,
    total_tokens: 160,
    completion_tokens_details: { reasoning_tokens: 25 },
    prompt_tokens_details: { cached_tokens: 20 },
    cache_creation_input_tokens: 10,
  }), {
    input_tokens: 90,
    output_tokens: 70,
    reasoning_tokens: 25,
    cached_input_tokens: 20,
    cache_write_tokens: 10,
    total_tokens: 160,
    inference_ms: 0,
  });
  assert.deepEqual(safeProviderUsage({
    input_tokens: 40,
    output_tokens: 12,
    total_tokens: 52,
    output_tokens_details: { reasoning_tokens: 4 },
    input_tokens_details: { cached_tokens: 8 },
  }), {
    input_tokens: 40,
    output_tokens: 12,
    reasoning_tokens: 4,
    cached_input_tokens: 8,
    cache_write_tokens: 0,
    total_tokens: 52,
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

test("Stagehand accepts only a bound gateway lease and never a provider credential", (t) => {
  const names = ["STAGEHAND_QA_INFERENCE_URL", "STAGEHAND_QA_INFERENCE_CAPABILITY", "STAGEHAND_QA_TASK_ID", "STAGEHAND_QA_RUN_ID", "STAGEHAND_QA_OPERATION", "MINIMAX_API_KEY", "AUTOMATION_CALLBACK_SECRET"];
  const previous = Object.fromEntries(names.map((name) => [name, process.env[name]]));
  t.after(() => {
    for (const name of names) {
      if (previous[name] === undefined) delete process.env[name]; else process.env[name] = previous[name];
    }
  });
  process.env.STAGEHAND_QA_INFERENCE_URL = "https://gateway.example.test/api/internal/automation/inference";
  process.env.STAGEHAND_QA_INFERENCE_CAPABILITY = "run-scoped-capability";
  process.env.AUTOMATION_CALLBACK_SECRET = "worker-callback-master-must-not-be-used";
  process.env.STAGEHAND_QA_TASK_ID = "task-1";
  process.env.STAGEHAND_QA_RUN_ID = "run-1";
  process.env.STAGEHAND_QA_OPERATION = "delivery.qa";
  process.env.MINIMAX_API_KEY = "must-not-be-used";
  const configured = modelConfiguration("LOCAL");
  assert.equal(configured.provider, "gateway");
  assert.equal(configured.isGateway, true);
  assert.equal(configured.inferenceURL, "https://gateway.example.test/api/internal/automation/inference");
  assert.equal(configured.inferenceCapability, "run-scoped-capability");
  assert.notEqual(configured.apiKey, configured.inferenceCapability);
  assert.notEqual(configured.apiKey, process.env.AUTOMATION_CALLBACK_SECRET);
  assert.notEqual(configured.apiKey, process.env.MINIMAX_API_KEY);

  process.env.STAGEHAND_QA_OPERATION = "product.ideate";
  assert.throws(() => modelConfiguration("LOCAL"), /bound delivery\.qa inference gateway lease/);
  process.env.STAGEHAND_QA_OPERATION = "delivery.qa";
  process.env.STAGEHAND_QA_INFERENCE_URL = "http://gateway.example.test/api/internal/automation/inference";
  assert.throws(() => modelConfiguration("LOCAL"), /must use HTTPS or loopback HTTP/);
});

test("Stagehand sends only the inference capability and refuses gateway redirects", async () => {
  let observed;
  const model = {
    inferenceURL: "https://gateway.example.test/api/internal/automation/inference",
    inferenceCapability: "run-scoped-capability",
  };
  await sendGatewayInference(model, { call_id: "d1a81060-32e1-4f55-a594-882f77a71ed1", task_id: "task", run_id: "run", operation: "delivery.qa" }, undefined, async (url, options) => {
    observed = { url, options };
    return { ok: true };
  });
  assert.equal(observed.url, model.inferenceURL);
  assert.equal(observed.options.redirect, "error");
  assert.equal(observed.options.headers["X-ITBEM-Inference-Capability"], "run-scoped-capability");
  assert.equal(observed.options.headers["X-Automation-Secret"], undefined);
});

test("gateway rejects invalid IDs and oversized inference payloads before network I/O", async () => {
  let fetchCalls = 0;
  const model = { inferenceURL: "https://gateway.example.test/infer", inferenceCapability: "run-scoped-capability" };
  const fetchImpl = async () => { fetchCalls += 1; return { ok: true }; };
  assert.throws(() => sendGatewayInference(model, { call_id: "not-a-uuid" }, undefined, fetchImpl), /call_id must be a UUID/);
  assert.throws(() => sendGatewayInference(model, { call_id: "d1a81060-32e1-4f55-a594-882f77a71ed1", content: "x".repeat(9_000) }, undefined, fetchImpl), /privacy size limit/);
  assert.equal(fetchCalls, 0);
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
  assert.deepEqual(evidence.failed_requests, ["failed_request"]);
  assert.equal(browserRuntimePassed(evidence), false);
});

test("performance timing preserves HTTP failures when Stagehand does not expose response events", async () => {
  const runtime = boundedBrowserRuntime();
  await runtime.capturePerformance({
    evaluate: async () => [{ name: "http://preview.local/api/tasks", initiator_type: "fetch", response_status: 503 }],
  });
  const evidence = runtime.evidence();
  assert.deepEqual(evidence.observed_network_sources, ["performance_timing"]);
  assert.deepEqual(evidence.failed_requests, ["failed_request"]);
  assert.equal(browserRuntimePassed(evidence), false);
});

test("known test values remain redacted while generic browser evidence is denied", (t) => {
  const key = "ITBEM_QA_REDACTION_TEST";
  const previous = process.env[key];
  process.env[key] = "qa-secret-value-123";
  t.after(() => {
    if (previous === undefined) delete process.env[key]; else process.env[key] = previous;
  });
  assert.equal(redactedInferenceText("Visible qa-secret-value-123 in browser"), "Visible [REDACTED_TEST_VALUE] in browser");
  assert.equal(redactedInferenceEvidence({ url: "http://preview.local/?value=qa-secret-value-123", nested: ["qa-secret-value-123"] }), "[OMITTED_UNTRUSTED_EVIDENCE]");
});

test("arbitrary private page text is omitted and semantic AI inference still goes through the gateway", async () => {
  const canaries = ["person-private-name-2417", "arbitrary-session-secret-9f3a", "private order total $8,432.19"];
  let gatewayRequest;
  let bodyTextReads = 0;
  const page = {
    evaluate: async () => ({
      document_ready: true,
      title_present: true,
      visible_text_present: true,
      primary_heading_count: 1,
      visible_primary_heading_count: 1,
      primary_action_count: 2,
      visible_primary_action_count: 1,
      form_count: 1,
      input_count: 3,
      title: canaries[0],
      body_text: canaries.join(" "),
      session: canaries[1],
    }),
    locator: () => ({ innerText: async () => { bodyTextReads += 1; return canaries.join(" "); } }),
    title: async () => canaries[0],
  };
  const model = {
    inferenceURL: "https://gateway.example.test/api/internal/automation/inference",
    inferenceCapability: "run-scoped-capability",
    taskID: "task-qa-1",
    runID: "run-qa-1",
    operation: "delivery.qa",
  };
  const receiptID = "beab9a68-019e-41c1-9552-bb3b95179bb7";
  const semantic = await assessWithGateway(page, model, {
    browser_e2e: {
      mode: "approved_test_flow",
      passed: true,
      cases: [{ id: canaries[0], title: canaries[1], steps: [{ id: "step", kind: "assert_text", passed: true, detail: canaries[2], text: canaries[0] }] }],
    },
    responsive: { passed: true, detail: canaries[2], overflow: { document_width: 412, viewport_width: 412 }, screenshot: canaries[0] },
    browser_runtime: { console_errors: [canaries[0]], failed_requests: [canaries[1]], observed_network_sources: ["performance_timing"] },
  }, async (url, options) => {
    gatewayRequest = { url, options, payload: JSON.parse(options.body) };
    return {
      ok: true,
      status: 200,
      json: async () => ({
        provider: "openrouter",
        model: "openrouter/cheap-test",
        call_id: gatewayRequest.payload.call_id,
        receipt_id: receiptID,
        content: JSON.stringify({ title: "", primary_heading: "", primary_action: "", blocking_issue: "" }),
        usage: {
          prompt_tokens: 42,
          completion_tokens: 8,
          total_tokens: 50,
          completion_tokens_details: { reasoning_tokens: 3 },
          prompt_tokens_details: { cached_tokens: 11 },
        },
      }),
    };
  });
  const transmitted = JSON.stringify(gatewayRequest.payload);
  for (const canary of canaries) assert.equal(transmitted.includes(canary), false);
  assert.equal(bodyTextReads, 0);
  assert.equal(gatewayRequest.url, model.inferenceURL);
  assert.equal(gatewayRequest.options.headers["X-ITBEM-Inference-Capability"], model.inferenceCapability);
  assert.equal(gatewayRequest.options.redirect, "error");
  assert.equal(gatewayRequest.options.headers["Authorization"], undefined);
  assert.ok(Buffer.byteLength(gatewayRequest.options.body, "utf8") <= 8_192);
  assert.match(gatewayRequest.payload.call_id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i);
  assert.equal(semantic.call.call_id, gatewayRequest.payload.call_id);
  assert.equal(semantic.call.receipt_id, receiptID);
  assert.equal(semantic.call.provider, "openrouter");
  assert.equal(semantic.call.model, "openrouter/cheap-test");
  assert.deepEqual(semantic.usage, {
    input_tokens: 42,
    output_tokens: 8,
    reasoning_tokens: 3,
    cached_input_tokens: 11,
    cache_write_tokens: 0,
    total_tokens: 50,
    inference_ms: 0,
  });
  assert.equal(gatewayRequest.payload.operation, "delivery.qa");
  assert.equal(gatewayRequest.payload.task_id, model.taskID);
  assert.equal(gatewayRequest.payload.run_id, model.runID);
  assert.equal(gatewayRequest.payload.max_completion_tokens, 1024);
  // The gateway authenticates this QA-only capability, then selects the
  // frozen provider/model route server-side. The transport placeholders are
  // not allowed to override that persisted route.
  assert.equal(gatewayRequest.payload.provider, "minimax");
  assert.equal(gatewayRequest.payload.model, "gateway-managed");
  assert.deepEqual(semantic.assessment, { title: "", primary_heading: "", primary_action: "", blocking_issue: "" });
});

test("gateway response must correlate its call and receipt before Stagehand accepts it", async () => {
  const page = { evaluate: async () => ({ document_ready: true }) };
  const model = {
    inferenceURL: "https://gateway.example.test/api/internal/automation/inference",
    inferenceCapability: "run-scoped-capability",
    taskID: "task-qa-1",
    runID: "run-qa-1",
    operation: "delivery.qa",
  };
  await assert.rejects(assessWithGateway(page, model, {}, async () => ({
    ok: true,
    status: 200,
    json: async () => ({ call_id: "00000000-0000-4000-8000-000000000000", receipt_id: "", content: "{}" }),
  })), /receipt correlation failed/);
});

test("billable rejected gateway responses retain the correlated receipt and native usage", async () => {
  const page = { evaluate: async () => ({ document_ready: true }) };
  const model = {
    inferenceURL: "https://gateway.example.test/api/internal/automation/inference",
    inferenceCapability: "run-scoped-capability",
    taskID: "task-qa-1",
    runID: "run-qa-1",
    operation: "delivery.qa",
  };
  let issuedCallID;
  await assert.rejects(assessWithGateway(page, model, {}, async (_url, options) => {
    issuedCallID = JSON.parse(options.body).call_id;
    return {
      ok: false,
      status: 422,
      json: async () => ({
        provider: "openrouter",
        model: "openrouter/cheap-test",
        call_id: issuedCallID,
        receipt_id: "beab9a68-019e-41c1-9552-bb3b95179bb7",
        usage: { prompt_tokens: 30, completion_tokens: 7, total_tokens: 37 },
      }),
    };
  }), (error) => {
    assert.equal(error.call.call_status, "failed");
    assert.equal(error.call.call_id, issuedCallID);
    assert.equal(error.call.receipt_id, "beab9a68-019e-41c1-9552-bb3b95179bb7");
    assert.equal(error.call.provider, "openrouter");
    assert.equal(error.call.model, "openrouter/cheap-test");
    assert.deepEqual(error.usage, {
      input_tokens: 30,
      output_tokens: 7,
      reasoning_tokens: 0,
      cached_input_tokens: 0,
      cache_write_tokens: 0,
      total_tokens: 37,
      inference_ms: 0,
    });
    return /Gateway inference rejected/.test(error.message);
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

test("sensitive or authenticated browser cases omit screenshots and redact test values from output", async (t) => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "itbem-stagehand-sensitive-case-"));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const envName = "ITBEM_QA_SCREENSHOT_CANARY";
  const previous = process.env[envName];
  const canary = "qa-sensitive-screen-canary-9081";
  process.env[envName] = canary;
  t.after(() => {
    if (previous === undefined) delete process.env[envName]; else process.env[envName] = previous;
  });

  const planPath = path.join(directory, "plan.json");
  await fs.writeFile(planPath, JSON.stringify({
    schema_version: 1,
    mode: "approved_test_flow",
    uses_authenticated_session: true,
    cases: [{
      id: "login",
      title: "Sensitive account flow",
      steps: [{ id: "fill-email", kind: "fill", selector: "#email", value_env: envName }],
    }],
  }));
  const plan = await loadBrowserPlan(planPath);
  assert.equal(requiresSensitiveScreenshotOmission(plan), true);

  let screenshotCalls = 0;
  let filledValue = "";
  let currentURL = "https://preview.example.test/login";
  let currentViewport = { width: 1440, height: 1200 };
  const page = {
    url: () => currentURL,
    goto: async (url) => { currentURL = url; },
    setViewportSize: async (width, height) => { currentViewport = { width, height }; },
    evaluate: async (operation) => {
      const source = operation.toString();
      if (source.includes("document_width")) return { document_width: currentViewport.width, viewport_width: currentViewport.width };
      if (source.includes("window.innerWidth")) return { ...currentViewport, device_scale_factor: 1 };
      if (source.includes("requestAnimationFrame")) return true;
      if (source.includes("const signature")) return "stable-layout";
      return null;
    },
    screenshot: async () => { screenshotCalls += 1; return Buffer.alloc(4096, 1); },
    locator: () => ({
      count: async () => 1,
      first: () => ({
        isVisible: async () => true,
        fill: async (value) => {
          filledValue = value;
          throw new Error(`Browser rejected ${value}`);
        },
      }),
    }),
  };
  const context = createPreviewURLContext(currentURL);
  const browserCases = await runBrowserCases(page, currentURL, plan, directory, { capturePerformance: async () => {} }, context);
  assert.equal(filledValue, canary);
  assert.equal(browserCases.cases[0].screenshot_status, "omitted_untrusted_page_content");
  assert.equal(browserCases.cases[0].screenshot_reason, "omitted_untrusted_page_content");
  assert.equal(browserCases.cases[0].before_screenshot, "");
  assert.equal(browserCases.cases[0].screenshot, "");
  assert.equal(browserCases.cases[0].steps[0].detail, "Browser QA step failed");

  const responsive = await runMobileResponsiveSmoke(page, currentURL, directory, { capturePerformance: async () => {} }, context, { suppressScreenshot: true });
  assert.equal(responsive.passed, true);
  assert.equal(responsive.screenshot, "");
  assert.equal(responsive.screenshot_status, "omitted_untrusted_page_content");
  assert.equal(screenshotCalls, 0);
  assert.deepEqual(await fs.readdir(directory), ["plan.json"]);

  const report = sanitizeReportValue({ browser_e2e: browserCases, responsive }, context);
  const output = JSON.stringify(report);
  assert.equal(output.includes(canary), false);
  assert.equal(output.includes("[REDACTED_TEST_VALUE]"), false);
  assert.equal(output.includes("Browser QA step failed"), true);
  assert.equal(requiresSensitiveScreenshotOmission({ mode: "read_only", cases: [{ uses_authenticated_session: false, steps: [] }] }), true);
});

test("public navigation also omits screenshots when page privacy cannot be proven", async (t) => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "itbem-stagehand-public-case-"));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const planPath = path.join(directory, "plan.json");
  await fs.writeFile(planPath, JSON.stringify({
    schema_version: 1,
    mode: "read_only",
    cases: [{ id: "public-home", title: "Public home", steps: [{ id: "open-home", kind: "navigate", path: "/home" }] }],
  }));
  const plan = await loadBrowserPlan(planPath);
  assert.equal(requiresSensitiveScreenshotOmission(plan), true);

  let screenshotCalls = 0;
  let currentURL = "https://preview.example.test/start";
  const page = {
    url: () => currentURL,
    goto: async (url) => { currentURL = url; },
    evaluate: async (operation) => {
      const source = operation.toString();
      if (source.includes("window.innerWidth")) return { width: 1440, height: 1200, device_scale_factor: 1 };
      if (source.includes("requestAnimationFrame")) return true;
      if (source.includes("const signature")) return "stable-layout";
      return null;
    },
    screenshot: async () => { screenshotCalls += 1; return Buffer.alloc(4096, 1); },
  };
  const context = createPreviewURLContext(currentURL);
  const result = await runBrowserCases(page, currentURL, plan, directory, { capturePerformance: async () => {} }, context);
  const testCase = result.cases[0];
  assert.equal(result.passed, true);
  assert.equal(testCase.screenshot_status, "omitted_untrusted_page_content");
  assert.equal(testCase.screenshot_reason, "omitted_untrusted_page_content");
  assert.equal(testCase.before_screenshot, "");
  assert.equal(testCase.screenshot, "");
  assert.equal(screenshotCalls, 0);
  assert.deepEqual(await fs.readdir(directory), ["plan.json"]);
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

test("signed preview URL is read from a private file, navigated intact, and redacted from browser evidence", async (t) => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "itbem-stagehand-signed-url-"));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const urlFile = path.join(directory, "preview-url.txt");
  const output = path.join(directory, "report.json");
  const signedURL = "https://preview.example.test/login?token=CANARY#CANARY";
  await fs.writeFile(urlFile, signedURL, { encoding: "utf8", mode: 0o600 });

  const parsed = await parseArguments(["--url-file", urlFile, "--output", output]);
  assert.equal(parsed.previewURL, signedURL);
  assert.equal(parsed.reportURL, "https://preview.example.test/login");
  let browserNavigation;
  await navigatePreview({ goto: async (url) => { browserNavigation = url; } }, parsed.previewURL);
  assert.equal(browserNavigation, signedURL);

  const pageState = { currentURL: signedURL, failNavigation: false };
  const page = {
    url: () => pageState.currentURL,
    goto: async (url) => {
      if (pageState.failNavigation) throw new Error(`Navigation failed for ${signedURL}`);
      pageState.currentURL = url;
    },
    evaluate: async (operation) => {
      const source = operation.toString();
      if (source.includes("window.innerWidth")) return { width: 1440, height: 1200, device_scale_factor: 1 };
      if (source.includes("requestAnimationFrame")) return true;
      if (source.includes("const signature")) return "stable-layout";
      return null;
    },
    screenshot: async () => Buffer.alloc(4096, 1),
  };
  const plan = { mode: "read_only", cases: [{ id: "open", title: "Open preview", steps: [{ id: "navigate", kind: "navigate", path: "/next" }] }] };
  const browserRuntime = boundedBrowserRuntime(parsed.urlContext);
  browserRuntime.capturePerformance = async () => {};
  const browserCases = await runBrowserCases(page, parsed.reportURL, plan, directory, browserRuntime, parsed.urlContext);
  assert.equal(browserCases.cases[0].before_screenshot_url, "");
  assert.equal(browserCases.cases[0].steps[0].url, "https://preview.example.test/next");

  pageState.currentURL = signedURL;
  pageState.failNavigation = true;
  const failedCases = await runBrowserCases(page, parsed.reportURL, plan, directory, browserRuntime, parsed.urlContext);
  assert.equal(failedCases.cases[0].steps[0].passed, false);
  assert.equal(failedCases.cases[0].steps[0].detail.includes("CANARY"), false);

  browserRuntime.recordFailedResponse({
    status: () => 500,
    request: () => ({ method: () => "GET", url: () => signedURL }),
  });
  const evidence = browserRuntime.evidence();
  assert.deepEqual(evidence.failed_requests, ["failed_request"]);
  const serialized = JSON.stringify({ report_url: parsed.reportURL, browserCases, failedCases, evidence });
  assert.equal(serialized.includes("CANARY"), false);
  const report = sanitizeReportValue({
    preview_url: signedURL,
    request: { url: signedURL },
    extraction: { error: `Navigation failed for ${signedURL}` },
    browser_runtime: evidence,
    evidence: { artifacts: [{ url: signedURL }] },
  }, parsed.urlContext);
  assert.equal(report.preview_url, parsed.reportURL);
  assert.equal(report.request.url, parsed.reportURL);
  assert.equal(JSON.stringify(report).includes("CANARY"), false);
});

test("signed literal URLs are rejected while unsigned --url literals remain compatible", async (t) => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "itbem-stagehand-url-args-"));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const output = path.join(directory, "report.json");
  await assert.rejects(
    parseArguments(["--url", "https://preview.example.test/login?token=CANARY#CANARY", "--output", output]),
    (error) => error.message === "signed preview URLs must be supplied with --url-file" && !error.message.includes("CANARY"),
  );
  await assert.rejects(
    parseArguments(["--url", "https://preview.example.test/login#CANARY", "--output", output]),
    /signed preview URLs must be supplied with --url-file/,
  );
  const parsed = await parseArguments(["--url", "https://preview.example.test/login", "--output", output]);
  assert.equal(parsed.previewURL, "https://preview.example.test/login");
  assert.equal(parsed.reportURL, "https://preview.example.test/login");
});

test("mobile browser seam receives full signed URL but emits sanitized URL and diagnostics", async (t) => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "itbem-stagehand-mobile-url-"));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const signedURL = "https://preview.example.test/login?token=CANARY#CANARY";
  const context = createPreviewURLContext(signedURL);
  const navigations = [];
  const pageState = { currentURL: "about:blank" };
  const page = {
    setViewportSize: async () => {},
    goto: async (url) => { navigations.push(url); pageState.currentURL = url; },
    url: () => pageState.currentURL,
    evaluate: async (operation) => {
      const source = operation.toString();
      if (source.includes("window.innerWidth")) return { width: 412, height: 915, device_scale_factor: 1 };
      if (source.includes("requestAnimationFrame")) return true;
      if (source.includes("const signature")) return "stable-layout";
      if (source.includes("document_width")) return { document_width: 412, viewport_width: 412 };
      return null;
    },
    screenshot: async () => Buffer.alloc(4096, 1),
  };
  const result = await runMobileResponsiveSmoke(page, context.navigationURL, directory, { capturePerformance: async () => {} }, context);
  assert.deepEqual(navigations, [signedURL]);
  assert.equal(result.url, "https://preview.example.test/login");
  assert.equal(JSON.stringify(result).includes("CANARY"), false);
});
