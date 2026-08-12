package steward

import (
	"fmt"
	"strconv"
	"strings"
)

// handleCommand routes slash commands with Go rules (no LLM cost).
func (s *Service) handleCommand(msg InboundMessage, text string) {
	parts := strings.Fields(text)
	cmd := strings.ToLower(parts[0])
	args := parts[1:]
	s.logger.Printf("command: channel=%d sender=%q cmd=%q args=%q", msg.ChannelID, msg.SenderID, cmd, stewardLogPreview(strings.Join(args, " ")))
	reply := func(t string) {
		_ = s.SendToChannel(msg.ChannelID, OutboundMessage{ThreadID: msg.ThreadID, Text: t})
	}
	switch cmd {
	case "/help":
		reply(s.helpText())
	case "/env":
		s.cmdEnv(msg, reply, args)
	case "/running":
		s.cmdRunning(reply)
	case "/sessions":
		s.cmdSessions(reply)
	case "/stop":
		s.cmdStop(msg, reply, args)
	case "/delete":
		s.cmdDelete(msg, reply, args)
	case "/task":
		s.cmdTask(reply, args)
	default:
		reply("未知命令：" + cmd + "\n" + s.helpText())
	}
}

func (s *Service) helpText() string {
	return `可用命令：
/help             帮助
/env list         查看环境
/env create <名称> <路径>   创建环境
/env delete <id或名称>      删除环境
/running          查看进行中的对话
/sessions         查看所有对话
/stop <会话id>    结束对话
/delete <会话id>  删除对话（需确认）
/task <会话id>    查看任务详情
其他消息将交给管家 Agent 处理。`
}

func (s *Service) cmdEnv(msg InboundMessage, reply func(string), args []string) {
	if len(args) == 0 {
		reply("用法：/env list | /env create <名称> <路径> | /env delete <id或名称>")
		return
	}
	switch strings.ToLower(args[0]) {
	case "list":
		envs, err := s.app.ListEnvironments()
		if err != nil {
			reply("查询环境失败：" + err.Error())
			return
		}
		if len(envs) == 0 {
			reply("暂无环境。使用 /env create <名称> <路径> 创建。")
			return
		}
		var b strings.Builder
		b.WriteString("环境列表：\n")
		for _, env := range envs {
			mark := " "
			if env.Active {
				mark = "*"
			}
			fmt.Fprintf(&b, "%s [%s] %s → %s\n", mark, env.ID, env.Name, env.Path)
			if env.Description != "" {
				fmt.Fprintf(&b, "    %s\n", env.Description)
			}
		}
		reply(b.String())
	case "create":
		if len(args) < 3 {
			reply("用法：/env create <名称> <路径>")
			return
		}
		name := args[1]
		path := strings.Join(args[2:], " ")
		envs, err := s.app.AddEnvironment(EnvironmentView{Name: name, Path: path})
		if err != nil {
			reply("创建环境失败：" + err.Error())
			return
		}
		for _, env := range envs {
			if env.Name == name {
				reply(fmt.Sprintf("已创建环境 [%s] %s → %s", env.ID, env.Name, env.Path))
				return
			}
		}
		reply("环境已创建。")
	case "delete":
		if len(args) < 2 {
			reply("用法：/env delete <id或名称>")
			return
		}
		target := args[1]
		envs, err := s.app.ListEnvironments()
		if err != nil {
			reply("查询环境失败：" + err.Error())
			return
		}
		var match *EnvironmentView
		for i := range envs {
			if envs[i].ID == target || envs[i].Name == target {
				match = &envs[i]
				break
			}
		}
		if match == nil {
			reply("未找到环境：" + target)
			return
		}
		if err := s.app.RemoveEnvironment(match.ID); err != nil {
			reply("删除环境失败：" + err.Error())
			return
		}
		reply(fmt.Sprintf("已删除环境 [%s] %s", match.ID, match.Name))
	default:
		reply("用法：/env list | /env create <名称> <路径> | /env delete <id或名称>")
	}
}

func (s *Service) cmdRunning(reply func(string)) {
	sessions, err := s.app.ListSessions()
	if err != nil {
		reply("查询失败：" + err.Error())
		return
	}
	var b strings.Builder
	count := 0
	for _, session := range sessions {
		if session.Status == "running" {
			count++
			fmt.Fprintf(&b, "#%d %s (%s)\n", session.ID, session.Title, session.Status)
		}
	}
	if count == 0 {
		reply("当前没有进行中的对话。")
		return
	}
	reply("进行中的对话：\n" + b.String())
}

func (s *Service) cmdSessions(reply func(string)) {
	sessions, err := s.app.ListSessions()
	if err != nil {
		reply("查询失败：" + err.Error())
		return
	}
	if len(sessions) == 0 {
		reply("暂无对话。")
		return
	}
	var b strings.Builder
	b.WriteString("全部对话：\n")
	limit := 30
	if len(sessions) < limit {
		limit = len(sessions)
	}
	for _, session := range sessions[:limit] {
		fmt.Fprintf(&b, "#%d %s (%s)\n", session.ID, session.Title, session.Status)
	}
	if len(sessions) > limit {
		fmt.Fprintf(&b, "…共 %d 个", len(sessions))
	}
	reply(b.String())
}

func (s *Service) cmdStop(msg InboundMessage, reply func(string), args []string) {
	if len(args) < 1 {
		reply("用法：/stop <会话id>")
		return
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		reply("会话 id 无效：" + args[0])
		return
	}
	if err := s.app.StopSession(id); err != nil {
		reply("结束对话失败：" + err.Error())
		return
	}
	s.FinishTask(id, "aborted", fmt.Sprintf("⏹ 对话 #%d 已结束。", id))
	reply(fmt.Sprintf("已结束对话 #%d。", id))
}

// cmdDelete asks for an explicit confirmation before deleting a session.
func (s *Service) cmdDelete(msg InboundMessage, reply func(string), args []string) {
	if len(args) < 1 {
		reply("用法：/delete <会话id>")
		return
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		reply("会话 id 无效：" + args[0])
		return
	}
	sessions, err := s.app.ListSessions()
	if err != nil {
		reply("查询失败：" + err.Error())
		return
	}
	title := fmt.Sprintf("#%d", id)
	for _, session := range sessions {
		if session.ID == id {
			title = fmt.Sprintf("#%d %s", session.ID, session.Title)
			break
		}
	}
	req := &PermissionRequest{
		RequestID: fmt.Sprintf("steward-delete-%d-%s", id, randomToken()[:16]),
		SessionID: id,
		Method:    "confirm",
		Title:     "删除对话确认",
		Body:      fmt.Sprintf("确定删除对话 %s？该操作不可恢复。回复「确认」继续，其他回复取消。", title),
		ChannelID: msg.ChannelID,
		Sender:    msg.SenderID,
		ThreadID:  msg.ThreadID,
		answerCh:  make(chan *PermissionAnswer, 1),
	}
	s.relayConfirm(req, id)
}

// relayConfirm routes a steward-initiated confirmation through the same
// permission pipeline; used by /delete and steward_ask_confirm.
func (s *Service) relayConfirm(req *PermissionRequest, deleteSessionID int64) {
	s.mu.Lock()
	s.pending[req.RequestID] = req
	s.mu.Unlock()
	_ = s.SendToChannel(req.ChannelID, OutboundMessage{ThreadID: req.ThreadID, Card: &CardPayload{
		Title: req.Title, Body: req.Body, Confirm: true,
	}})
	s.launchBackground(func() {
		s.mu.Lock()
		ctx := s.runCtx
		s.mu.Unlock()
		select {
		case answer := <-req.answerCh:
			s.handleConfirmAnswer(req, answer, deleteSessionID)
		case <-timeAfter(s.permissionTimeout):
			s.mu.Lock()
			delete(s.pending, req.RequestID)
			s.mu.Unlock()
			s.replyAfter(req, "⏰ 确认超时，操作已取消。")
		case <-ctx.Done():
		}
	})
}

func (s *Service) handleConfirmAnswer(req *PermissionRequest, answer *PermissionAnswer, deleteSessionID int64) {
	s.mu.Lock()
	delete(s.pending, req.RequestID)
	s.mu.Unlock()
	if answer.Cancelled || (answer.Confirmed != nil && !*answer.Confirmed) {
		s.replyAfter(req, "已取消操作。")
		return
	}
	if err := s.app.DeleteSession(deleteSessionID); err != nil {
		s.replyAfter(req, "删除对话失败："+err.Error())
		return
	}
	s.replyAfter(req, fmt.Sprintf("已删除对话 #%d。", deleteSessionID))
	s.finishTaskBySession(deleteSessionID, "aborted", "🗑 对话已删除。")
}

// replyAfter sends a reply back to the confirmation origin.
func (s *Service) replyAfter(req *PermissionRequest, text string) {
	_ = s.SendToChannel(req.ChannelID, OutboundMessage{ThreadID: req.ThreadID, Text: text})
}

func (s *Service) cmdTask(reply func(string), args []string) {
	if len(args) < 1 {
		reply("用法：/task <会话id>")
		return
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		reply("会话 id 无效：" + args[0])
		return
	}
	sessions, err := s.app.ListSessions()
	if err != nil {
		reply("查询失败：" + err.Error())
		return
	}
	for _, session := range sessions {
		if session.ID == id {
			reply(fmt.Sprintf("#%d %s\n状态：%s\n耗时：%dms", session.ID, session.Title, session.Status, session.ExecDurationMs))
			return
		}
	}
	reply("未找到会话 #" + args[0])
}

// finishTaskBySession completes a task for a deleted session.
func (s *Service) finishTaskBySession(sessionID int64, status, result string) {
	s.mu.Lock()
	task, ok := s.tasks[sessionID]
	delete(s.tasks, sessionID)
	delete(s.managed, sessionID)
	s.mu.Unlock()
	if !ok {
		_ = s.store.DeleteBotTaskBySessionID(sessionID)
		return
	}
	now := unixMillisNow()
	_ = s.store.UpdateBotTask(task.ID, map[string]any{"status": status, "result_text": result, "finished_at": now})
	s.emitTask(task, "finished")
}
