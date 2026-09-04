package service

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
)

const (
	AdvancedRestart         = "restartServer"
	AdvancedDeleteRecordDir = "deleteRecordDirectory"
	AdvancedDeleteSnapDir   = "deleteSnapDirectory"
	AdvancedBroadcast       = "broadcastMessage"
)

var advancedPeriodPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var advancedMP4NamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+\.mp4$`)
var advancedSnapNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+\.jpe?g$`)

func advancedFailure(msg string) map[string]any {
	return map[string]any{"code": -1, "msg": msg}
}

func (h *Hub) AdvancedOperation(nodeID, user, action string, q url.Values) map[string]any {
	if q == nil {
		q = url.Values{}
	}
	action = strings.TrimSpace(action)
	if h == nil || h.audit == nil {
		return advancedFailure("操作审计不可用，已拒绝执行")
	}
	if !isAdvancedAction(action) {
		result := advancedFailure("不支持的高级操作")
		h.recordAudit(nodeID, user, action, action, false, asString(result["msg"]))
		return result
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		result := advancedFailure("unknown node")
		h.recordAudit(nodeID, user, action, advancedTarget(action, q), false, asString(result["msg"]))
		return result
	}
	ApplyZLMIni(&n)
	target := advancedTarget(action, q)
	operationID, err := h.beginAuditedMutation(nodeID, user, action, target)
	if err != nil {
		return advancedFailure("操作审计预写失败，已拒绝执行")
	}
	finish := func(result map[string]any, attempted bool) map[string]any {
		out := h.finishAuditedMutation(nodeID, user, action, target, operationID, result, attempted)
		if strings.Contains(asString(out["msg"]), "审计结果写入失败") {
			if attempted {
				return advancedFailure("上游可能已执行但审计结果写入失败")
			}
			return advancedFailure("操作未执行且审计结果写入失败")
		}
		return sanitizeAdvancedResult(out)
	}

	vals, err := validateAdvanced(action, q)
	if err != nil {
		return finish(advancedFailure(err.Error()), false)
	}

	localPath, localErr := "", error(nil)
	switch action {
	case AdvancedDeleteRecordDir:
		localPath, localErr = deleteLocalRecordDir(n, vals.Get("app"), vals.Get("stream"), vals.Get("period"), vals.Get("name"))
	case AdvancedDeleteSnapDir:
		localPath, localErr = deleteLocalSnapDir(n, vals.Get("app"), vals.Get("stream"), vals.Get("file"))
	}
	if localErr != nil && !os.IsNotExist(localErr) {
		logger.Warnf("高级删除本地失败 action=%s target=%s: %v", action, target, localErr)
		return finish(advancedFailure(localErr.Error()), false)
	}

	result, err := h.zlm.callPOST(n, action, vals)
	if action == AdvancedDeleteRecordDir {
		result, err = interpretDeleteRecordDirectory(result, err)
	}
	if localPath != "" {
		msg := advancedSuccessMessage(action)
		if redacted := redactAbsolutePath(localPath); redacted != "" {
			msg += " " + redacted
		}
		logger.Infor("高级删除已落地 action=%s path=%s", action, redactAbsolutePath(localPath))
		return finish(map[string]any{"code": 0, "msg": msg, "path": redactAbsolutePath(localPath)}, true)
	}
	if err != nil {
		msg := err.Error()
		if result != nil && asString(result["msg"]) != "" {
			msg = asString(result["msg"])
		}
		if action == AdvancedDeleteRecordDir || action == AdvancedDeleteSnapDir {
			msg = "未找到可删除的目录（录制为 mp4/record/{app}/{stream}，截图为 snap/{stream}）"
		}
		if result == nil {
			result = advancedFailure(msg)
		} else {
			result["code"] = -1
			result["msg"] = msg
		}
		return finish(result, true)
	}
	if result == nil {
		result = map[string]any{"code": 0, "msg": advancedSuccessMessage(action)}
	} else if strings.TrimSpace(asString(result["msg"])) == "" {
		result["msg"] = advancedSuccessMessage(action)
		result["code"] = 0
	} else {
		result["code"] = 0
	}
	return finish(result, true)
}

func isAdvancedAction(action string) bool {
	switch action {
	case AdvancedRestart, AdvancedDeleteRecordDir, AdvancedDeleteSnapDir:
		return true
	default:
		return false
	}
}

func advancedSuccessMessage(action string) string {
	switch action {
	case AdvancedRestart:
		return "已请求重启 MediaServer"
	case AdvancedDeleteRecordDir:
		return "已删除录像目录"
	case AdvancedDeleteSnapDir:
		return "已删除截图目录"
	default:
		return "操作成功"
	}
}

func advancedTarget(action string, q url.Values) string {
	switch action {
	case AdvancedRestart:
		return "MediaServer"
	case AdvancedDeleteRecordDir:
		return strings.Join([]string{
			defaultVhost(q.Get("vhost")), q.Get("app"), q.Get("stream"), q.Get("period"), q.Get("name"),
		}, "/")
	case AdvancedDeleteSnapDir:
		return strings.Join([]string{
			defaultVhost(q.Get("vhost")), q.Get("app"), q.Get("stream"), q.Get("file"),
		}, "/")
	default:
		return action
	}
}

func defaultVhost(vhost string) string {
	vhost = strings.TrimSpace(vhost)
	if vhost == "" {
		return "__defaultVhost__"
	}
	return vhost
}

func validateAdvanced(action string, q url.Values) (url.Values, error) {
	switch action {
	case AdvancedRestart:
		return url.Values{}, nil
	case AdvancedDeleteRecordDir:
		out, err := validateRecordIdentity(q, false)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(q.Get("customized_path")) != "" {
			return nil, fmt.Errorf("不允许自定义删除根路径")
		}
		period := strings.TrimSpace(q.Get("period"))
		if period != "" {
			if !advancedPeriodPattern.MatchString(period) {
				return nil, fmt.Errorf("period 必须是 YYYY-MM-DD，留空则删除该流全部录像目录")
			}
			out.Set("period", period)
		}
		if name := strings.TrimSpace(q.Get("name")); name != "" {
			if !advancedMP4NamePattern.MatchString(name) || strings.Contains(name, "..") {
				return nil, fmt.Errorf("name 只能是普通 mp4 文件名")
			}
			out.Set("name", name)
		}
		return out, nil
	case AdvancedDeleteSnapDir:
		out, err := validateRecordIdentity(q, false)
		if err != nil {
			return nil, err
		}
		if file := strings.TrimSpace(q.Get("file")); file != "" {
			base := path.Base(strings.ReplaceAll(file, "\\", "/"))
			if base != file || !advancedSnapNamePattern.MatchString(file) || strings.Contains(file, "..") {
				return nil, fmt.Errorf("file 只能是普通 jpg 文件名")
			}
			out.Set("file", file)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("不支持的高级操作")
	}
}

func deleteLocalRecordDir(n config.Node, app, stream, period, name string) (string, error) {
	app, stream = strings.TrimSpace(app), strings.TrimSpace(stream)
	if app == "" || stream == "" {
		return "", fmt.Errorf("app/stream 必填")
	}
	var bases []string
	seen := map[string]bool{}
	add := func(p string) {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" || p == "." || seen[p] {
			return
		}
		seen[p] = true
		bases = append(bases, p)
	}
	if n.MP4Save != "" {
		add(filepath.Join(n.MP4Save, "record", app, stream))
		add(filepath.Join(n.MP4Save, "rec", app, stream))
		add(filepath.Join(n.MP4Save, app, stream))
	}
	if n.Root != "" {
		add(filepath.Join(n.Root, "mp4", "record", app, stream))
		add(filepath.Join(n.Root, "record", app, stream))
	}
	for _, d := range streamSearchDirs(n, app, stream) {
		slash := filepath.ToSlash(d)
		if strings.Contains(slash, "/record/") || strings.Contains(slash, "/rec/") ||
			(n.MP4Save != "" && insideRoot(n.MP4Save, d)) {
			add(d)
		}
	}
	return removeFirstExisting(n, bases, period, name, true)
}

func deleteLocalSnapDir(n config.Node, app, stream, file string) (string, error) {
	stream = snapStreamName(stream)
	if stream == "_" {
		return "", fmt.Errorf("stream 必填")
	}
	root := snapRootOf(n)
	bases := []string{
		filepath.Join(root, stream),
		filepath.Join(root, strings.TrimSpace(app), stream),
	}
	return removeFirstExisting(n, bases, "", file, false)
}

func removeFirstExisting(n config.Node, bases []string, period, leaf string, record bool) (string, error) {
	roots := advancedDeleteRoots(n, record)
	if len(roots) == 0 {
		return "", os.ErrNotExist
	}
	var lastMissing error = os.ErrNotExist
	for _, base := range bases {
		target := base
		if period != "" {
			target = filepath.Join(target, period)
		}
		if leaf != "" {
			target = filepath.Join(target, leaf)
		}
		abs, err := filepath.Abs(target)
		if err != nil {
			continue
		}
		if !insideAnyRoot(roots, abs) || isProtectedRoot(roots, abs) {
			continue
		}
		st, err := os.Stat(abs)
		if err != nil {
			lastMissing = err
			continue
		}
		if leaf != "" && st.IsDir() {
			continue
		}
		if err := os.RemoveAll(abs); err != nil {
			return "", fmt.Errorf("删除失败: %v", err)
		}
		return abs, nil
	}
	return "", lastMissing
}

func advancedDeleteRoots(n config.Node, record bool) []string {
	var roots []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		roots = append(roots, abs)
	}
	if record {
		add(n.MP4Save)
		add(n.Root)
		if n.MP4Save != "" {
			add(filepath.Dir(n.MP4Save))
		}
	} else {
		add(snapRootOf(n))
		add(n.Root)
	}
	return roots
}

func insideAnyRoot(roots []string, abs string) bool {
	for _, root := range roots {
		if insideRoot(root, abs) {
			return true
		}
	}
	return false
}

func isProtectedRoot(roots []string, abs string) bool {
	clean := filepath.Clean(abs)
	for _, root := range roots {
		if filepath.Clean(root) == clean {
			return true
		}
	}
	return false
}

func interpretDeleteRecordDirectory(result map[string]any, err error) (map[string]any, error) {
	if result == nil {
		return result, err
	}
	if _, isBool := result["code"].(bool); isBool {
		if asTruthy(result["code"]) {
			result["code"] = 0
			return result, nil
		}
		return result, fmt.Errorf("删除录像目录失败")
	}
	if err != nil && asString(result["path"]) != "" && asFloat(result["code"]) == 1 {
		result["code"] = 0
		return result, nil
	}
	return result, err
}

func sanitizeAdvancedResult(result map[string]any) map[string]any {
	if result == nil {
		return advancedFailure("空结果")
	}
	msg := zlmSecretQueryPattern.ReplaceAllString(asString(result["msg"]), "[REDACTED]")
	out := map[string]any{
		"code": result["code"],
		"msg":  msg,
	}
	if asFloat(out["code"]) == 0 && strings.TrimSpace(msg) == "" {
		out["msg"] = "操作成功"
	}
	if raw := strings.TrimSpace(asString(result["path"])); raw != "" {
		out["path"] = redactAbsolutePath(raw)
	}
	return out
}

func redactAbsolutePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/mp4/"); i >= 0 {
		return p[i+1:]
	}
	if i := strings.LastIndex(p, "/snap/"); i >= 0 {
		return p[i+1:]
	}
	return path.Base(strings.TrimRight(p, "/"))
}
