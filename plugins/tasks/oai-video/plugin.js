// OpenAI-compatible video upstream: any provider that serves POST /v1/videos,
// GET /v1/videos/{id} and GET /v1/videos/{id}/content. Third-party gateways and
// aggregation services can rename the models below to the names they expose.

const SIZES = ["720x1280", "1280x720", "1792x1024", "1024x1792"];
const DEFAULT_SECONDS = 4;
const DEFAULT_SIZE = "720x1280";

const STATUSES = {
  queued: "QUEUED",
  pending: "QUEUED",
  submitted: "QUEUED",
  not_start: "QUEUED",
  in_progress: "IN_PROGRESS",
  processing: "IN_PROGRESS",
  running: "IN_PROGRESS",
  completed: "SUCCESS",
  succeeded: "SUCCESS",
  success: "SUCCESS",
  failed: "FAILURE",
  cancelled: "FAILURE",
  canceled: "FAILURE",
  expired: "FAILURE",
};

export const meta = {
  apiVersion: 1,
  key: "oai-video",
  name: "OpenAI-Compatible Video",
  icon: "text:OV",
  description: {
    en: "Video generation through any upstream that serves the OpenAI /v1/videos API",
    zh: "通过任意提供 OpenAI /v1/videos 接口的上游生成视频",
  },
  version: "1.0.0",
  author: { name: "QuantumNous" },
  models: ["video-1", "video-1-pro"],
  fetchMode: "per_task",
  // Both facts drive billing: seconds is the duration price, size selects the
  // resolution tier. Keep the keys stable so one price expression fits every
  // channel that runs this plugin.
  usageSchema: {
    seconds: {
      type: "number",
      unit: "second",
      description: { en: "Video generation unit price", zh: "视频生成单价" },
    },
    size: {
      enum: SIZES,
      enumLabels: {
        "720x1280": { en: "720x1280", zh: "720x1280" },
        "1280x720": { en: "1280x720", zh: "1280x720" },
        "1792x1024": { en: "1792x1024", zh: "1792x1024" },
        "1024x1792": { en: "1024x1792", zh: "1024x1792" },
      },
      description: { en: "Output video size", zh: "输出视频尺寸" },
    },
  },
  usageExamples: [
    { label: "4s 720x1280", facts: { seconds: 4, size: "720x1280" } },
    { label: "8s 1280x720", facts: { seconds: 8, size: "1280x720" } },
    { label: "12s 1792x1024", facts: { seconds: 12, size: "1792x1024" } },
  ],
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }, "openai_video"],
};

function trimmed(value) {
  return String(value || "").trim();
}

function plainObject(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : null;
}

// Returns the accepted duration, or null when the value is absent or outside
// the host-owned second ceiling. Callers decide whether null is an error.
function parseSeconds(value) {
  if (value === undefined || value === null || value === "") return null;
  const n = Number(value);
  if (!Number.isFinite(n) || !Number.isInteger(n) || n <= 0 || n > 3600) return null;
  return n;
}

function parseSize(value) {
  const raw = trimmed(value);
  return SIZES.indexOf(raw) >= 0 ? raw : null;
}

function checkedSeconds(value) {
  const n = parseSeconds(value);
  if (value !== undefined && value !== null && value !== "" && n === null)
    throw new Error("seconds must be an integer between 1 and 3600");
  return n;
}

function checkedSize(value) {
  const size = parseSize(value);
  if (value !== undefined && value !== null && value !== "" && size === null)
    throw new Error("size must be one of " + SIZES.join(", "));
  return size;
}

function responsesInput(req) {
  const texts = [],
    images = [];
  const input = req.input;
  if (typeof input === "string") texts.push(input);
  else if (Array.isArray(input)) {
    for (const item of input) {
      if (typeof item === "string") {
        texts.push(item);
        continue;
      }
      if (!item || typeof item !== "object" || Array.isArray(item)) continue;
      const content = item.content === undefined ? [item] : Array.isArray(item.content) ? item.content : [item.content];
      for (const part of content) {
        if (typeof part === "string") {
          texts.push(part);
          continue;
        }
        if (!part || typeof part !== "object" || Array.isArray(part)) continue;
        if (["input_text", "text"].includes(part.type) && typeof part.text === "string") texts.push(part.text);
        if (["input_image", "image_url"].includes(part.type)) {
          let image = part.image_url;
          if (image && typeof image === "object") image = image.url;
          if (trimmed(image)) images.push(trimmed(image));
        }
      }
    }
  }
  return {
    prompt: texts
      .filter(function (text) {
        return trimmed(text);
      })
      .join("\n"),
    images: images,
  };
}

function responsesVideoText(ctx) {
  const artifact = ctx && ctx.artifacts && ctx.artifacts.video;
  const url = trimmed(artifact && artifact.url);
  if (!url) throw new Error("video artifact is unavailable");
  const escaped = url.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return '<video controls src="' + escaped + '"></video>';
}

export function buildSubmitRequest(ctx) {
  const req = ctx.requestBody || {};
  const prompt = trimmed(req.prompt);
  if (!prompt) throw new Error("field prompt is required");
  const action = ctx.action === "remix" ? "remix" : ctx.action;
  const headers = { Authorization: "Bearer " + ctx.apiKey };
  if (action === "remix") {
    headers["Content-Type"] = "application/json";
    return {
      url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.originTaskId) + "/remix",
      method: "POST",
      headers: headers,
      body: { model: ctx.upstreamModel, prompt: prompt },
      action: action,
    };
  }
  const body = Object.assign({}, req);
  body.model = ctx.upstreamModel;
  body.prompt = prompt;
  if (body.seconds === undefined && body.duration !== undefined) body.seconds = body.duration;
  delete body.duration;
  const seconds = parseSeconds(body.seconds);
  if (seconds !== null) body.seconds = seconds;
  const size = parseSize(body.size);
  if (size !== null) body.size = size;
  const files = Array.isArray(ctx.files) ? ctx.files : [];
  if (files.length) {
    const parts = [];
    for (const key of Object.keys(body)) {
      const value = body[key];
      if (value === undefined || value === null || typeof value === "object") continue;
      parts.push({ name: key, value: String(value) });
    }
    if (body.metadata && typeof body.metadata === "object" && !Array.isArray(body.metadata)) {
      parts.push({ name: "metadata", value: JSON.stringify(body.metadata) });
    }
    for (const file of files) parts.push({ name: file.field, fileRef: file.ref, filename: file.filename });
    return { url: ctx.baseUrl + "/v1/videos", method: "POST", headers: headers, bodyType: "multipart", parts: parts, action: action };
  }
  headers["Content-Type"] = "application/json";
  return { url: ctx.baseUrl + "/v1/videos", method: "POST", headers: headers, body: body, action: action };
}

export function parseSubmitResponse(ctx, resp) {
  const body = plainObject((resp || {}).body) || {};
  const taskId = trimmed(body.id || body.task_id);
  if (!taskId) throw new Error("upstream response is missing the video id");
  return { taskId: taskId, taskData: body };
}

// Remix length is chosen by the vendor, so the reservation stays empty and the
// completion facts settle the charge.
export function extractUsage(ctx) {
  if (ctx.action === "remix") return {};
  const req = ctx.requestBody || {};
  const seconds = parseSeconds(req.seconds !== undefined ? req.seconds : req.duration);
  const size = parseSize(req.size);
  return {
    seconds: seconds === null ? DEFAULT_SECONDS : seconds,
    size: size === null ? DEFAULT_SIZE : size,
  };
}

export function extractUsageOnComplete(_task, _taskResult, body) {
  const payload = plainObject(body) || {};
  const facts = {};
  const seconds = parseSeconds(payload.seconds !== undefined ? payload.seconds : payload.duration);
  if (seconds !== null) facts.seconds = seconds;
  const size = parseSize(payload.size);
  if (size !== null) facts.size = size;
  return facts;
}

export function buildQueryRequest(ctx) {
  return {
    url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId),
    method: "GET",
    headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}

export function parseTaskResult(ctx, body) {
  const payload = plainObject(body) || {};
  const status = STATUSES[trimmed(payload.status).toLowerCase()];
  const result = { status: status || "UNKNOWN" };
  if (!status) result.reason = "unrecognized status: " + String(payload.status || "");
  const progress = Number(payload.progress);
  if (Number.isFinite(progress) && progress > 0 && progress < 100) result.progress = Math.floor(progress) + "%";
  if (status === "FAILURE") {
    const message = plainObject(payload.error) ? trimmed(payload.error.message) : trimmed(payload.error);
    result.reason = message || "task failed";
  }
  return result;
}

export function listArtifacts(task) {
  return task && task.status === "SUCCESS" ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  return {
    url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.upstreamTaskId) + "/content",
    method: ctx.clientRequest.method,
    headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}

function videoEnvelope(task) {
  const statuses = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "in_progress", SUCCESS: "completed", FAILURE: "failed" };
  const properties = task.properties || {};
  const output = {
    id: task.task_id,
    object: "video",
    model: properties.origin_model_name || "",
    status: statuses[task.status] || "unknown",
    progress: Number(String(task.progress || "0").replace("%", "")),
    created_at: Number(task.created_at || 0),
  };
  const completedAt = Number(task.finished_at || task.updated_at || 0);
  if (completedAt > 0) output.completed_at = completedAt;
  if (task.status === "FAILURE") output.error = { code: "video_generation_failed", message: task.fail_reason || "The video generation task failed." };
  return output;
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value;
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      const model = trimmed(req.model);
      if (!model) throw new Error("model is required");
      if (req.input !== undefined && typeof req.input !== "string" && !Array.isArray(req.input)) throw new Error("input must be a string or array");
      if (req.images !== undefined && !Array.isArray(req.images)) throw new Error("images must be an array");
      if (req.metadata !== undefined && !plainObject(req.metadata)) throw new Error("metadata must be an object");
      const input = responsesInput(req);
      const prompt = input.prompt || trimmed(req.prompt);
      if (!prompt) throw new Error("input is required");
      const images = [];
      for (const image of [req.image, req.input_reference].concat(req.images || [], input.images)) {
        if (trimmed(image) && !images.includes(trimmed(image))) images.push(trimmed(image));
      }
      const requestBody = { model: model, prompt: prompt };
      if (images.length) requestBody.input_reference = images[0];
      if (Object.prototype.hasOwnProperty.call(req, "seconds")) requestBody.seconds = req.seconds;
      else if (Object.prototype.hasOwnProperty.call(req, "duration")) requestBody.seconds = req.duration;
      if (Object.prototype.hasOwnProperty.call(req, "size")) requestBody.size = req.size;
      if (Object.prototype.hasOwnProperty.call(req, "metadata")) requestBody.metadata = req.metadata;
      checkedSeconds(requestBody.seconds);
      checkedSize(requestBody.size);
      return { kind: "submit", model: model, action: images.length ? "image_to_video" : "text_to_video", requestBody: requestBody };
    },
    renderEvents: function (ctx, task, previousState) {
      const status = String(task.status || "UNKNOWN").toUpperCase();
      const value = Number(String(task.progress || "").replace("%", ""));
      const progress = Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
      const state = { status: status, progress: progress };
      if (status === "SUCCESS") {
        const text = responsesVideoText(ctx);
        const events = previousState && previousState.status === status ? [] : [{ type: "output", data: text }];
        return { events: events, state: state, done: true };
      }
      if (status === "FAILURE")
        return { events: [{ type: "error", code: "task_failed", message: task.fail_reason || "task failed" }], state: state, done: true };
      if (previousState && previousState.status === status && previousState.progress === progress) return { events: [], state: state, done: false };
      const event = { type: "progress", message: status.toLowerCase() };
      if (progress !== null) event.progress = progress;
      return { events: [event], state: state, done: false };
    },
    renderFinal: function (ctx, _task) {
      return {
        output: [
          {
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", text: responsesVideoText(ctx), annotations: [], logprobs: [] }],
          },
        ],
        metadata: { vendor: "oai-video" },
      };
    },
  },
};

protocols.openai_video = {
  decodeRequest: function (ctx) {
    if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) throw new Error("JSON or multipart body required");
    if (ctx.body.kind === "json") {
      const req = plainObject(ctx.body.value);
      if (!req) throw new Error("JSON object required");
      const seconds = req.seconds === undefined ? req.duration : req.seconds;
      checkedSeconds(seconds);
      checkedSize(req.size);
      const requestBody = Object.assign({}, req, { model: ctx.model });
      delete requestBody.duration;
      if (seconds !== undefined) requestBody.seconds = Number(seconds);
      return {
        kind: "submit",
        model: ctx.model,
        action: req.input_reference || req.image ? "image_to_video" : "text_to_video",
        requestBody: requestBody,
      };
    }
    const fields = ctx.body.fields || {};
    const req = {};
    for (const name of Object.keys(fields)) {
      const values = fields[name] || [];
      if (values.length > 1) throw new Error(name + " must be provided once");
      req[name] = values[0];
    }
    let hasInputReferenceFile = false;
    for (const file of ctx.body.files || []) {
      if (file.field !== "input_reference") throw new Error("unexpected file field: " + file.field);
      if (hasInputReferenceFile) throw new Error("input_reference must be provided once");
      hasInputReferenceFile = true;
    }
    if (req.metadata !== undefined) {
      let parsed;
      try {
        parsed = JSON.parse(req.metadata);
      } catch (e) {
        throw new Error("metadata must be a JSON object string");
      }
      if (!plainObject(parsed)) throw new Error("metadata must be a JSON object string");
      req.metadata = parsed;
    }
    const seconds = req.seconds === undefined ? req.duration : req.seconds;
    delete req.duration;
    if (seconds !== undefined) req.seconds = Number(seconds);
    checkedSeconds(req.seconds);
    checkedSize(req.size);
    return {
      kind: "submit",
      model: ctx.model,
      action: hasInputReferenceFile || req.input_reference || req.image ? "image_to_video" : "text_to_video",
      requestBody: Object.assign({}, req, { model: ctx.model }),
    };
  },
  render: function (ctx, task) {
    const data = plainObject(task && task.data);
    return data || videoEnvelope(task);
  },
};
