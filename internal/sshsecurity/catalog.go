package sshsecurity

// BuiltinCapabilities returns the immutable built-in capability catalog.
func BuiltinCapabilities() []Capability {
	intParam := func(min, max int64) ParamSpec {
		return ParamSpec{Type: ParamInt, Required: true, Min: min, Max: max}
	}
	container := ParamSpec{Type: ParamContainerName, Required: true}
	gitRef := ParamSpec{Type: ParamGitRef, Required: true}
	service := ParamSpec{Type: ParamServiceName, Required: true}
	return []Capability{
		{Name: "system.uname", Group: "system", Description: "查看系统与内核信息", Executable: "uname", Args: []string{"-a"}, Permission: EffectAllow},
		{Name: "system.uptime", Group: "system", Description: "查看运行时长与负载", Executable: "uptime", Args: []string{}, Permission: EffectAllow},
		{Name: "system.disk_usage", Group: "system", Description: "查看磁盘使用情况", Executable: "df", Args: []string{"-h"}, Permission: EffectAllow},
		{Name: "system.memory", Group: "system", Description: "查看内存使用情况", Executable: "free", Args: []string{"-m"}, Permission: EffectAllow},
		{Name: "system.processes", Group: "system", Description: "查看进程列表", Executable: "ps", Args: []string{"aux"}, Permission: EffectAllow},
		{Name: "system.journal", Group: "system", Description: "查看 systemd 服务日志", Executable: "journalctl", Args: []string{"-u", "{service}", "-n", "{lines}", "--no-pager"}, Params: map[string]ParamSpec{"service": service, "lines": intParam(1, 5000)}, Permission: EffectAllow},
		{Name: "service.status", Group: "service", Description: "查看 systemd 服务状态", Executable: "systemctl", Args: []string{"status", "{service}", "--no-pager"}, Params: map[string]ParamSpec{"service": service}, Permission: EffectAllow},
		{Name: "service.restart", Group: "service", Description: "重启 systemd 服务", Executable: "systemctl", Args: []string{"restart", "{service}"}, Params: map[string]ParamSpec{"service": service}, Permission: EffectAsk},
		{Name: "service.stop", Group: "service", Description: "停止 systemd 服务", Executable: "systemctl", Args: []string{"stop", "{service}"}, Params: map[string]ParamSpec{"service": service}, Permission: EffectAsk},
		{Name: "git.status", Group: "git", Description: "查看工作区状态", Executable: "git", Args: []string{"-C", "{resourceWorkDir}", "status", "--short", "--branch"}, Permission: EffectAllow},
		{Name: "git.log", Group: "git", Description: "查看提交历史", Executable: "git", Args: []string{"-C", "{resourceWorkDir}", "log", "--oneline", "--decorate", "-n", "{limit}"}, Params: map[string]ParamSpec{"limit": intParam(1, 500)}, Permission: EffectAllow},
		{Name: "git.diff", Group: "git", Description: "查看指定引用的差异摘要", Executable: "git", Args: []string{"-C", "{resourceWorkDir}", "diff", "--stat", "{ref}"}, Params: map[string]ParamSpec{"ref": gitRef}, Permission: EffectAllow},
		{Name: "git.show", Group: "git", Description: "查看指定提交摘要", Executable: "git", Args: []string{"-C", "{resourceWorkDir}", "show", "--stat", "--oneline", "{ref}"}, Params: map[string]ParamSpec{"ref": gitRef}, Permission: EffectAllow},
		{Name: "git.branches", Group: "git", Description: "查看本地分支", Executable: "git", Args: []string{"-C", "{resourceWorkDir}", "branch", "--list", "-vv"}, Permission: EffectAllow},
		{Name: "git.tags", Group: "git", Description: "查看标签", Executable: "git", Args: []string{"-C", "{resourceWorkDir}", "tag", "--list"}, Permission: EffectAllow},
		{Name: "git.remotes", Group: "git", Description: "查看远程仓库", Executable: "git", Args: []string{"-C", "{resourceWorkDir}", "remote", "-v"}, Permission: EffectAllow},
		{Name: "git.fetch", Group: "git", Description: "更新远程引用", Executable: "git", Args: []string{"-C", "{resourceWorkDir}", "fetch", "--all", "--prune"}, Permission: EffectAsk},
		{Name: "git.pull", Group: "git", Description: "拉取当前分支", Executable: "git", Args: []string{"-C", "{resourceWorkDir}", "pull", "--ff-only"}, Permission: EffectAsk},
		{Name: "docker.ps", Group: "docker", Description: "查看容器列表", Executable: "docker", Args: []string{"ps", "--all", "--no-trunc"}, Permission: EffectAllow},
		{Name: "docker.images", Group: "docker", Description: "查看镜像列表", Executable: "docker", Args: []string{"images", "--no-trunc"}, Permission: EffectAllow},
		{Name: "docker.logs", Group: "docker", Description: "查看容器日志", Executable: "docker", Args: []string{"logs", "--tail", "{lines}", "{container}"}, Params: map[string]ParamSpec{"lines": intParam(1, 5000), "container": container}, Permission: EffectAllow},
		{Name: "docker.inspect", Group: "docker", Description: "查看容器详情", Executable: "docker", Args: []string{"inspect", "{container}"}, Params: map[string]ParamSpec{"container": container}, Permission: EffectAllow},
		{Name: "docker.stats", Group: "docker", Description: "查看一次容器资源统计", Executable: "docker", Args: []string{"stats", "--no-stream", "{container}"}, Params: map[string]ParamSpec{"container": container}, Permission: EffectAllow},
		{Name: "docker.restart", Group: "docker", Description: "重启容器", Executable: "docker", Args: []string{"restart", "{container}"}, Params: map[string]ParamSpec{"container": container}, Permission: EffectAsk},
		{Name: "docker.stop", Group: "docker", Description: "停止容器", Executable: "docker", Args: []string{"stop", "{container}"}, Params: map[string]ParamSpec{"container": container}, Permission: EffectAsk},
		{Name: "docker.rm", Group: "docker", Description: "删除容器（内置禁止）", Executable: "docker", Args: []string{"rm", "{container}"}, Params: map[string]ParamSpec{"container": container}, Permission: EffectDeny},
		{Name: "docker.exec", Group: "docker", Description: "容器内执行命令（内置禁止）", Executable: "docker", Args: []string{"exec", "{container}"}, Params: map[string]ParamSpec{"container": container}, Permission: EffectDeny},
		{Name: "shell.raw", Group: "advanced", Description: "原始 Shell 命令（默认禁止，启用后强制询问）", Executable: "", Args: []string{"{command}"}, Params: map[string]ParamSpec{"command": {Type: ParamString, Required: true, MaxLength: 4096}}, Permission: EffectDeny, TimeoutSeconds: 60},
	}
}

// CapabilityByName resolves a built-in or resource-local custom capability.
func CapabilityByName(resource Resource, name string) (Capability, bool) {
	for _, capability := range BuiltinCapabilities() {
		if capability.Name == name {
			capability.Normalize()
			return capability, true
		}
	}
	for _, capability := range resource.CustomCapabilities {
		if capability.Name == name {
			capability.Normalize()
			return capability, true
		}
	}
	return Capability{}, false
}
