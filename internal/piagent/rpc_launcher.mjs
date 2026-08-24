import { pathToFileURL } from "node:url";
import path from "node:path";

function parseArgs(argv) {
  const parsed = {
    extensions: [],
    skills: [],
    excludeTools: [],
    appendSystemPrompt: [],
  };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    const value = () => {
      if (index + 1 >= argv.length) throw new Error(`${arg} requires a value`);
      index += 1;
      return argv[index];
    };
    switch (arg) {
      case "--mode":
        if (value() !== "rpc") throw new Error("CodingTo Pi launcher only supports RPC mode");
        break;
      case "--provider": parsed.provider = value(); break;
      case "--model": parsed.model = value(); break;
      case "--session-dir": parsed.sessionDir = value(); break;
      case "--session-id": parsed.sessionId = value(); break;
      case "--session": parsed.sessionPath = value(); break;
      case "--extension": parsed.extensions.push(value()); break;
      case "--skill": parsed.skills.push(value()); break;
      case "--exclude-tools": parsed.excludeTools.push(...value().split(",").map((item) => item.trim()).filter(Boolean)); break;
      case "--append-system-prompt": parsed.appendSystemPrompt.push(value()); break;
      case "--no-builtin-tools": parsed.noBuiltinTools = true; break;
      case "--no-session": parsed.noSession = true; break;
      case "--no-extensions": parsed.noExtensions = true; break;
      default: throw new Error(`Unsupported CodingTo Pi launcher argument: ${arg}`);
    }
  }
  return parsed;
}

async function main() {
  const [sdkEntry, authPath, agentDir, ...argv] = process.argv.slice(2);
  if (!sdkEntry || !authPath || !agentDir) throw new Error("Missing CodingTo Pi launcher paths");
  const sdkURL = pathToFileURL(sdkEntry).href;
  const {
    ModelRuntime,
    SessionManager,
    SettingsManager,
    createAgentSessionFromServices,
    createAgentSessionRuntime,
    createAgentSessionServices,
    resolveCliModel,
    runRpcMode,
  } = await import(sdkURL);
  const { applyHttpProxySettings, configureHttpDispatcher } = await import(
    pathToFileURL(path.join(path.dirname(sdkEntry), "core", "http-dispatcher.js")).href
  );
  const { ProjectTrustStore, hasTrustRequiringProjectResources } = await import(
    pathToFileURL(path.join(path.dirname(sdkEntry), "core", "trust-manager.js")).href
  );
  const parsed = parseArgs(argv);
  const initialCwd = process.cwd();
  const resolvedAgentDir = path.resolve(agentDir);
  const resolvedAuthPath = path.resolve(authPath);
  const extensions = parsed.extensions.map((item) => path.resolve(initialCwd, item));
  const skills = parsed.skills.map((item) => path.resolve(initialCwd, item));
  const trustStore = new ProjectTrustStore(resolvedAgentDir);

  let sessionManager;
  if (parsed.noSession) {
    sessionManager = SessionManager.inMemory(initialCwd, parsed.sessionId ? { id: parsed.sessionId } : undefined);
  } else if (parsed.sessionPath) {
    sessionManager = SessionManager.open(path.resolve(parsed.sessionPath), parsed.sessionDir, initialCwd);
  } else {
    sessionManager = SessionManager.create(initialCwd, parsed.sessionDir, parsed.sessionId ? { id: parsed.sessionId } : undefined);
  }

  const createRuntime = async ({ cwd, sessionManager: targetSessionManager, sessionStartEvent }) => {
    const globalSettings = SettingsManager.create(cwd, resolvedAgentDir, { projectTrusted: false });
    const storedTrust = trustStore.get(cwd);
    const defaultTrust = globalSettings.getDefaultProjectTrust();
    const projectTrusted = !hasTrustRequiringProjectResources(cwd)
      || (storedTrust !== null ? storedTrust : defaultTrust === "always");
    const settingsManager = SettingsManager.create(cwd, resolvedAgentDir, { projectTrusted });
    const modelRuntime = await ModelRuntime.create({
      authPath: resolvedAuthPath,
      modelsPath: path.join(resolvedAgentDir, "models.json"),
    });
    const services = await createAgentSessionServices({
      cwd,
      agentDir: resolvedAgentDir,
      modelRuntime,
      settingsManager,
      resourceLoaderOptions: {
        additionalExtensionPaths: extensions,
        additionalSkillPaths: skills,
        noExtensions: parsed.noExtensions,
        appendSystemPrompt: parsed.appendSystemPrompt,
      },
    });
    const resolved = resolveCliModel({
      cliProvider: parsed.provider,
      cliModel: parsed.model,
      modelRuntime: services.modelRuntime,
    });
    if (resolved.error) throw new Error(resolved.error);
    const created = await createAgentSessionFromServices({
      services,
      sessionManager: targetSessionManager,
      sessionStartEvent,
      model: resolved.model,
      thinkingLevel: resolved.thinkingLevel,
      noTools: parsed.noBuiltinTools ? "builtin" : undefined,
      excludeTools: parsed.excludeTools,
    });
    return { ...created, services, diagnostics: services.diagnostics };
  };

  const runtime = await createAgentSessionRuntime(createRuntime, {
    cwd: sessionManager.getCwd(),
    agentDir: resolvedAgentDir,
    sessionManager,
  });
  const errors = runtime.diagnostics.filter((item) => item.type === "error");
  if (errors.length > 0) throw new Error(errors.map((item) => item.message).join("; "));
  const settings = runtime.services.settingsManager;
  applyHttpProxySettings(settings.getGlobalSettings().httpProxy);
  configureHttpDispatcher(settings.getHttpIdleTimeoutMs());
  void runtime.services.modelRuntime.refresh().catch(() => {});
  await runRpcMode(runtime);
}

main().catch((error) => {
  console.error(error instanceof Error ? error.stack || error.message : String(error));
  process.exitCode = 1;
});
