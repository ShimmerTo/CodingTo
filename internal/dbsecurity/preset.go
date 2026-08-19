package dbsecurity

// SafePreset 只读预设：读操作允许，其余全部拒绝。
func SafePreset() []Rule {
	return []Rule{
		{ID: "safe-read", Action: ActionRead, Effect: EffectAllow},
		{ID: "safe-write", Action: ActionWrite, Effect: EffectDeny},
		{ID: "safe-schema", Action: ActionSchema, Effect: EffectDeny},
		{ID: "safe-admin", Action: ActionAdmin, Effect: EffectDeny},
		{ID: "safe-transaction", Action: ActionTransaction, Effect: EffectDeny},
		{ID: "safe-external", Action: ActionExternal, Effect: EffectDeny},
		{ID: "safe-unknown", Action: ActionUnknown, Effect: EffectDeny},
	}
}

// DevelopmentPreset 开发预设：读允许；写需确认且必须带 WHERE；
// 结构变更与逃逸口拒绝；未识别语句拒绝。
func DevelopmentPreset() []Rule {
	return []Rule{
		{ID: "dev-read", Action: ActionRead, Effect: EffectAllow},
		{ID: "dev-write", Action: ActionWrite, Effect: EffectConfirm,
			Conditions: Condition{RequireWhere: true},
			Reason:     "写入语句需要用户确认，且必须带 WHERE 条件"},
		{ID: "dev-schema", Action: ActionSchema, Effect: EffectDeny},
		{ID: "dev-admin", Action: ActionAdmin, Effect: EffectDeny},
		{ID: "dev-transaction", Action: ActionTransaction, Effect: EffectConfirm,
			Reason: "显式事务控制需要用户确认"},
		{ID: "dev-external", Action: ActionExternal, Effect: EffectDeny},
		{ID: "dev-unknown", Action: ActionUnknown, Effect: EffectDeny},
	}
}

// FullPreset 完全预设：读/写允许；结构变更、管理与逃逸口需确认；
// 未识别语句仍然拒绝（所有预设默认拒绝 unknown）。
func FullPreset() []Rule {
	return []Rule{
		{ID: "full-read", Action: ActionRead, Effect: EffectAllow},
		{ID: "full-write", Action: ActionWrite, Effect: EffectAllow},
		{ID: "full-schema", Action: ActionSchema, Effect: EffectConfirm,
			Reason: "结构变更（DDL）需要用户确认"},
		{ID: "full-admin", Action: ActionAdmin, Effect: EffectConfirm,
			Reason: "管理类语句需要用户确认"},
		{ID: "full-transaction", Action: ActionTransaction, Effect: EffectAllow},
		{ID: "full-external", Action: ActionExternal, Effect: EffectConfirm,
			Reason: "语句包含文件/外部逃逸口，需要用户确认"},
		{ID: "full-unknown", Action: ActionUnknown, Effect: EffectDeny},
	}
}
