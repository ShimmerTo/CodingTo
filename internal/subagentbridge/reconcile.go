package subagentbridge

import (
	"os"
	"path/filepath"
	"time"

	"codingto/internal/applog"
)

// OrphanAbortError 是孤儿子 agent run 被对账改写为 aborted 时写入的错误说明。
// 应用重启后子 agent 的 bridge 与其子 Pi 进程必然已消失，run.json 上残留的
// running 状态无法再被任何进程推进（follow-up 永远不会送达），只能以终态收尾。
const OrphanAbortError = "子 Agent 进程在应用重启时丢失，运行被标记为已中止"

// ReconcileOrphanedRuns 扫描会话目录下的 subagents/*/run.json，把仍标记为
// running 的 run 原子改写为终态 aborted。子 agent 的 bridge 与子 Pi 进程都
// 是 CodingTo 主进程的后代，主进程重启后它们不可能存活，因此启动时发现
// running 记录必然是孤儿：其 follow-up 永远不会送达，保留 running 会让会话
// 永久停留在"等待子 agent"状态。返回被改写（或补写）的记录数量。
//
// 读取失败（缺失或损坏）的 run.json 同样补写最小 aborted 记录：
// runningSubagentCount 把任何读错误都计为运行中，若这里跳过不处理，损坏的
// 记录会让会话永远等不到终态。损坏时原字段无法解析保留，但写终态能保证
// 会话可继续推进。
func ReconcileOrphanedRuns(sessionDir string) (int, error) {
	root := filepath.Join(sessionDir, "subagents")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	now := time.Now().UnixMilli()
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDir := filepath.Join(root, entry.Name())
		path := filepath.Join(runDir, "run.json")
		record, readErr := ReadRunRecord(path)
		if readErr != nil {
			// run.json 缺失或损坏（bridge 初始化中被打断、写入中断、磁盘残留）
			// 都视为运行中：补写一条最小 aborted 记录，与 runningSubagentCount
			// 的防御语义保持一致。损坏的记录无法保留原字段，但写终态能保证
			// 会话不会永久停留在"等待子 agent"状态。
			if !os.IsNotExist(readErr) {
				applog.Infof("[subagent %s] reconcile unreadable run record (%v), marking aborted", entry.Name(), readErr)
			}
			record = RunRecord{
				Version: 1, RunID: entry.Name(), Status: "aborted",
				EndedAt: now, Error: OrphanAbortError, Files: []RunFile{},
			}
			if err := writeRunRecord(runDir, record); err != nil {
				applog.Infof("[subagent %s] reconcile missing run record: %v", entry.Name(), err)
				continue
			}
			count++
			continue
		}
		if record.Status != "running" {
			continue
		}
		record.Status = "aborted"
		record.EndedAt = now
		record.Error = OrphanAbortError
		if err := writeRunRecord(runDir, record); err != nil {
			applog.Infof("[subagent %s] reconcile running run record: %v", entry.Name(), err)
			continue
		}
		count++
	}
	return count, nil
}
