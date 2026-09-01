// Builds ordered and paginated session groups for the application sidebar.
export function buildSessionGroups({
  environments,
  tasks,
  archivedTaskIds,
  workspaceOrder,
  visibleSessionCounts,
  pageSize,
  ungroupedLabel
}) {
  const groups = environments.map(workspace => ({
    id: workspace.id,
    name: workspace.name || workspace.path,
    env: workspace,
    all: [],
    visible: [],
    remaining: 0
  }))
  const orphan = { id: '', name: ungroupedLabel, env: null, all: [], visible: [], remaining: 0 }

  for (const task of tasks) {
    if (archivedTaskIds.has(task.id) || task.isSteward) continue
    const group = groups.find(item => item.id === task.environmentId) || orphan
    group.all.push(task)
  }

  const ordered = [...groups]
  if (orphan.all.length) ordered.push(orphan)
  if (workspaceOrder.length) {
    const byId = new Map(ordered.map(group => [group.id, group]))
    const prioritized = []
    for (const id of workspaceOrder) {
      const group = byId.get(id)
      if (group) {
        prioritized.push(group)
        byId.delete(id)
      }
    }
    ordered.length = 0
    ordered.push(...prioritized, ...byId.values())
  }

  for (const group of ordered) {
    group.all.sort((left, right) => (
      (Number(right.updatedAt) || Number(right.createdAt) || 0)
      - (Number(left.updatedAt) || Number(left.createdAt) || 0)
    ))
    const limit = visibleSessionCounts[group.id] || pageSize
    group.visible = group.all.slice(0, limit)
    group.remaining = group.all.length - group.visible.length
  }
  return ordered
}
