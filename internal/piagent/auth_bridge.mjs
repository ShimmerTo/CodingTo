import { pathToFileURL } from "node:url";
import { readFile } from "node:fs/promises";
import path from "node:path";

function emit(event) {
	process.stdout.write(`${JSON.stringify(event)}\n`);
}

function publicError(error) {
	const message = error instanceof Error ? error.message : String(error ?? "");
	if (/cancel|abort/i.test(message)) return "cancelled";
	if (/missing authorization code/i.test(message)) return "callback_failed";
	if (/not found|cannot find|module/i.test(message)) return "sdk_unavailable";
	if (/credential|auth\.json|lock|persist|permission|eacces|eperm/i.test(message)) return "credential_store_failed";
	return "oauth_failed";
}

function waitForBrowserCallback(prompt, signal) {
	return new Promise((resolve, reject) => {
		let settled = false;
		const finish = (fn, value) => {
			if (settled) return;
			settled = true;
			fn(value);
		};
		const promptAborted = () => finish(resolve, "");
		const loginAborted = () => finish(reject, new Error("Login cancelled"));
		if (prompt.signal?.aborted) {
			promptAborted();
			return;
		}
		if (signal?.aborted) {
			loginAborted();
			return;
		}
		prompt.signal?.addEventListener("abort", promptAborted, { once: true });
		signal?.addEventListener("abort", loginAborted, { once: true });
	});
}

async function main() {
	const [sdkEntry, operation, agentDir] = process.argv.slice(2);
	if (!sdkEntry || !operation || !agentDir) throw new Error("Missing bridge arguments");

	const { ModelRuntime } = await import(pathToFileURL(sdkEntry).href);
	if (!ModelRuntime || typeof ModelRuntime.create !== "function") {
		throw new Error("Pi ModelRuntime module is unavailable");
	}
	const runtime = await ModelRuntime.create({
		authPath: path.join(agentDir, "auth.json"),
		modelsPath: path.join(agentDir, "models.json"),
		allowModelNetwork: false,
	});

	if (operation === "logout") {
		await runtime.logout("openai-codex");
		emit({ type: "completed" });
		return;
	}
	if (operation === "usage") {
		const resolved = await runtime.getAuth("openai-codex");
		const accessToken = resolved?.auth?.apiKey;
		if (!accessToken) throw new Error("ChatGPT credential is unavailable");
		const headers = new Headers(resolved.auth.headers ?? {});
		headers.set("Authorization", `Bearer ${accessToken}`);
		headers.set("Accept", "application/json");
		headers.set("originator", "codingto");
		const credentials = JSON.parse(await readFile(path.join(agentDir, "auth.json"), "utf8"));
		const accountId = credentials?.["openai-codex"]?.accountId;
		if (typeof accountId === "string" && accountId) headers.set("ChatGPT-Account-Id", accountId);
		const response = await fetch("https://chatgpt.com/backend-api/wham/usage", { headers });
		if (!response.ok) throw new Error(`ChatGPT usage request failed (${response.status})`);
		const body = await response.json();
		const planType = typeof body?.plan_type === "string" ? body.plan_type.trim().toLowerCase() : "";
		const primaryWindow = body?.rate_limit?.primary_window;
		const secondaryWindow = body?.rate_limit?.secondary_window;
		const weeklyWindow = secondaryWindow ?? primaryWindow;
		if (!weeklyWindow) throw new Error("ChatGPT weekly usage is unavailable");
		const toWindow = (value) => {
			if (!value || typeof value !== "object") return {};
			const usedPercent = Number(value.used_percent);
			const resetAfter = Number(value.reset_after_seconds);
			const resetAt = Number(value.reset_at);
			const resetSeconds = Number.isFinite(resetAfter)
				? Math.max(0, Math.round(resetAfter))
				: Number.isFinite(resetAt)
					? Math.max(0, Math.round(resetAt - Date.now() / 1000))
					: 0;
			return {
				percent: Number.isFinite(usedPercent) ? Math.min(100, Math.max(0, 100 - usedPercent)) : 0,
				resetSeconds,
			};
		};
		emit({
			type: "completed",
			usage: {
				planType,
				rolling: secondaryWindow ? toWindow(primaryWindow) : {},
				weekly: toWindow(weeklyWindow),
			},
		});
		return;
	}
	if (operation !== "login") throw new Error("Unsupported bridge operation");

	const controller = new AbortController();
	const credential = await runtime.login("openai-codex", "oauth", {
		signal: controller.signal,
		notify(event) {
			if (event?.type === "auth_url") {
				emit({ type: "auth_url", url: event.url });
			}
		},
		prompt(prompt) {
			if (prompt.type === "select") return Promise.resolve("browser");
			if (prompt.type === "manual_code") return waitForBrowserCallback(prompt, controller.signal);
			return Promise.reject(new Error("Unsupported OAuth prompt"));
		},
	});
	emit({
		type: "completed",
		accountId: typeof credential.accountId === "string" ? credential.accountId : "",
		expires: typeof credential.expires === "number" ? credential.expires : 0,
	});
}

main().catch((error) => {
	emit({ type: "error", code: publicError(error) });
	process.exitCode = 1;
});
