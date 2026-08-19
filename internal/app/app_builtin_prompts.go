package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxBuiltinPromptBytes = 32 * 1024

const defaultPlanPrompt = `# CodingTo Plan Policy

在执行任何会修改系统状态的操作之前，必须先征求用户确认。

- 涉及改动的操作包括但不限于：编辑或写入文件、执行会产生副作用的终端命令、git 写入，以及调用会改变外部状态的接口。
- 执行前先调用 codingto_plan_present，把完整有序的执行计划展示到计划面板，并等待 confirmed === true。
- 不要在聊天回复中重复输出编号计划；计划由工具统一渲染。
- confirmed === false 表示用户取消：立即停止，不继续执行，也不要自动再次展示计划。
- 每完成或重新打开一个步骤，调用 codingto_plan_update，使计划面板与实际进度一致。`

const defaultSessionStartupPrompt = `- 当开始执行任务以及确定或改变方向时，必须在思考结束后使用中文描述接下来的思路或方向
- 主动加载项目根目录 AGENTS.md作为遵守的规则或注意项
- 思考结果或过程使用中文输出
- 动手之前必须就当前任务的边界以及现存逻辑进行提问，禁止自行脑补`

const defaultMemoryPrompt = `# CodingTo Memory Policy

Memory 分为三层：

- User Memory：仅保存从多个任务中推断出的跨项目通用偏好或注意事项，或在多个任务中反复出现的同一偏好。
- Project History：位于当前项目 .codingto/history，仅在任务确实需要历史信息时调用 codingto_memory_search 读取。
- Project Rules：位于当前工作目录 AGENTS.md 的”项目规则”小节；每次任务完成前检查，只有形成长期有效的项目注意事项时才调用 codingto_memory_update_project_rules。
- 正常任务先读取真实代码和当前项目状态，不把历史记录作为推理前提。
- 不要保存项目事实、一次性要求、客户原话、敏感信息或可从代码读取的内容。不确定时不写入。
- 更新 User Memory 优先调用 codingto_memory_patch_user，仅增删简短条目并保留已有内容；只有用户明确要求整体替换时才调用 codingto_memory_update_user。
- 仅当任务完成且形成值得长期保留的项目改动时，调用 codingto_memory_write_history；记录应保留文件路径、技术标识符、关键词、原因和最终方案。普通或无持久价值的任务不要写入历史。`

// BuiltinPromptConfigSnapshot is the effective global behavior prompt for one
// configurable built-in. Tool schemas and host protocol markers deliberately
// stay in code so editing the prompt cannot break structured communication.
type BuiltinPromptConfigSnapshot struct {
	Prompt         string `json:"prompt"`
	DefaultPrompt  string `json:"defaultPrompt"`
	IsDefault      bool   `json:"isDefault"`
	MaxPromptBytes int    `json:"maxPromptBytes"`
}

type SaveBuiltinPromptConfigRequest struct {
	Prompt         string `json:"prompt"`
	RestoreDefault bool   `json:"restoreDefault"`
}

func (a *App) builtinPromptPath(key string) string {
	return filepath.Join(a.store.Dir(), "prompts", key+".md")
}

func defaultBuiltinPrompt(key string) (string, bool) {
	switch key {
	case "plan":
		return defaultPlanPrompt, true
	case "memory":
		return defaultMemoryPrompt, true
	case "session_startup":
		return defaultSessionStartupPrompt, true
	default:
		return "", false
	}
}

func (a *App) getBuiltinPromptConfig(key string) (BuiltinPromptConfigSnapshot, error) {
	fallback, ok := defaultBuiltinPrompt(key)
	if !ok {
		return BuiltinPromptConfigSnapshot{}, fmt.Errorf("unsupported built-in prompt: %s", key)
	}
	content, err := os.ReadFile(a.builtinPromptPath(key))
	if err != nil {
		if !os.IsNotExist(err) {
			return BuiltinPromptConfigSnapshot{}, fmt.Errorf("read %s prompt: %w", key, err)
		}
		return BuiltinPromptConfigSnapshot{Prompt: fallback, DefaultPrompt: fallback, IsDefault: true, MaxPromptBytes: maxBuiltinPromptBytes}, nil
	}
	return BuiltinPromptConfigSnapshot{Prompt: strings.TrimSuffix(string(content), "\n"), DefaultPrompt: fallback, IsDefault: false, MaxPromptBytes: maxBuiltinPromptBytes}, nil
}

func (a *App) saveBuiltinPromptConfig(key string, req SaveBuiltinPromptConfigRequest) (BuiltinPromptConfigSnapshot, error) {
	if _, ok := defaultBuiltinPrompt(key); !ok {
		return BuiltinPromptConfigSnapshot{}, fmt.Errorf("unsupported built-in prompt: %s", key)
	}
	path := a.builtinPromptPath(key)
	if req.RestoreDefault {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return BuiltinPromptConfigSnapshot{}, fmt.Errorf("restore %s prompt: %w", key, err)
		}
		return a.getBuiltinPromptConfig(key)
	}
	if len([]byte(req.Prompt)) > maxBuiltinPromptBytes {
		return BuiltinPromptConfigSnapshot{}, fmt.Errorf("%s prompt exceeds %d bytes", key, maxBuiltinPromptBytes)
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return BuiltinPromptConfigSnapshot{}, fmt.Errorf("create prompt directory: %w", err)
	}
	content := strings.TrimSpace(req.Prompt)
	if content != "" {
		content += "\n"
	}
	if err := writePrivateFileAtomic(path, []byte(content)); err != nil {
		return BuiltinPromptConfigSnapshot{}, fmt.Errorf("write %s prompt: %w", key, err)
	}
	return a.getBuiltinPromptConfig(key)
}

// effectiveBuiltinPrompt returns the effective prompt content for the given key.
// It reads from <storeDir>/prompts/<key>.md, falling back to the hardcoded default
// when the file does not exist.
func effectiveBuiltinPrompt(storeDir, key string) string {
	content, err := os.ReadFile(filepath.Join(storeDir, "prompts", key+".md"))
	if err != nil {
		if fallback, ok := defaultBuiltinPrompt(key); ok {
			return fallback
		}
		return ""
	}
	return strings.TrimSuffix(string(content), "\n")
}

func (a *App) GetSessionStartupPromptConfig() (BuiltinPromptConfigSnapshot, error) {
	return a.getBuiltinPromptConfig("session_startup")
}

func (a *App) SaveSessionStartupPromptConfig(req SaveBuiltinPromptConfigRequest) (BuiltinPromptConfigSnapshot, error) {
	return a.saveBuiltinPromptConfig("session_startup", req)
}

func (a *App) GetPlanConfig() (BuiltinPromptConfigSnapshot, error) {
	return a.getBuiltinPromptConfig("plan")
}

func (a *App) SavePlanConfig(req SaveBuiltinPromptConfigRequest) (BuiltinPromptConfigSnapshot, error) {
	return a.saveBuiltinPromptConfig("plan", req)
}
