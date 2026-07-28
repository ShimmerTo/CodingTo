// Build the extension badges for one agent from the backend's unified
// snapshot. Keep this pure so the "newly installed package is visible"
// contract has a focused regression test.
export function collectAgentExtensions(agent, snapshot = {}) {
  const builtins = snapshot?.builtins?.[agent.id] || []
  const recommended = snapshot?.recommended?.[agent.id] || []
  const packages = snapshot?.packages?.[agent.id] || []
  const items = []
  const knownPaths = new Set()

  for (const status of builtins) {
    items.push({
      key: status.key,
      name: status.name || status.key,
      active: !!agent.builtin?.[status.key],
      installed: !!status.installed,
    })
    if (status.sourcePath) knownPaths.add(status.sourcePath)
  }

  for (const status of recommended) {
    // browser-native has no config flag; its per-agent package installation is
    // the enablement state. Other recommended extensions use agent config.
    const active = !!agent.recommended?.[status.key]
      || (status.key === 'browser-native' && !!status.installed)
    items.push({
      key: status.key,
      name: status.name || status.key,
      active,
      installed: !!status.installed,
    })
    if (status.sourcePath) knownPaths.add(status.sourcePath)
  }

  for (const status of packages) {
    if (status.sourcePath && knownPaths.has(status.sourcePath)) continue
    items.push({
      key: status.key,
      name: status.name || status.key,
      active: true,
      installed: !!status.installed,
    })
  }
  return items
}
