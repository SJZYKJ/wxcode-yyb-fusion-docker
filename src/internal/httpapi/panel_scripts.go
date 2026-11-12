package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ---------- 面板脚本目录（下游定制） ----------
//
// 「运行管理」页与「青龙脚本目录可见范围」都要求网关能列出面板脚本目录里的文件，
// 而上游 0.3.x 的 PanelDriver 只暴露了 env / cron / log 三类能力。这里以「可选接口」
// 的方式补回脚本目录读取：
//
//   - qingLongDriver 实现了真正的 /open/scripts/files 读取（含空目录递归、扁平数组兜底）；
//   - daidai/arcadia 等没有这个能力的驱动不实现该接口，panelManager 会返回错误，
//     调用方按既有逻辑降级为「从定时任务反推脚本」。
//
// 采用可选接口而不是往 PanelDriver 里加方法，是为了不改上游的驱动契约：
// 新增渠道驱动无需为了编译而写一个空实现。

// qingLongScript 是面板脚本目录里的一个脚本文件。
//
// Path 是相对脚本根目录的完整路径（如 `code脚本/绿鼻子.js`），它同时是网关给账号
// 挂任务时写进 `task <Path>` 的内容；Dir 是它所在目录，根目录下的脚本 Dir 为空。
type qingLongScript struct {
	Path string
	Name string
	Dir  string
}

func newQingLongScript(path string) qingLongScript {
	path = strings.Trim(strings.ReplaceAll(strings.TrimSpace(path), `\`, "/"), "/")
	dir, name := "", path
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		dir, name = strings.Trim(path[:idx], "/"), path[idx+1:]
	}
	return qingLongScript{Path: path, Name: name, Dir: dir}
}

// qingLongScriptNode 是青龙 /open/scripts/files 返回的目录树节点。
type qingLongScriptNode struct {
	Title    string               `json:"title"`
	Value    string               `json:"value"`
	Key      string               `json:"key"`
	Type     string               `json:"type"`
	Parent   string               `json:"parent"`
	Children []qingLongScriptNode `json:"children"`
}

// scriptLister 是驱动可选实现的脚本目录枚举能力。
type scriptLister interface {
	ListScripts(ctx context.Context) ([]qingLongScript, error)
}

// ListScripts 与 listScripts 等价，保留大写版本便于外部/测试调用。
func (p *panelManager) ListScripts(ctx context.Context) ([]qingLongScript, error) {
	return p.listScripts(ctx)
}

func (p *panelManager) listScripts(ctx context.Context) ([]qingLongScript, error) {
	if p == nil {
		return nil, fmt.Errorf("面板未配置")
	}
	lister, ok := p.getDriver().(scriptLister)
	if !ok {
		return nil, fmt.Errorf("当前面板不支持列出脚本目录")
	}
	return lister.ListScripts(ctx)
}

// ListScripts 读取脚本目录树并展开成脚本文件列表。
func (d *qingLongDriver) ListScripts(ctx context.Context) ([]qingLongScript, error) {
	var raw json.RawMessage
	if err := d.request(ctx, http.MethodGet, "/open/scripts/files", nil, &raw); err != nil {
		// 少数版本把脚本列表挂在 /open/scripts 上
		if fallbackErr := d.request(ctx, http.MethodGet, "/open/scripts", nil, &raw); fallbackErr != nil {
			return nil, err
		}
	}
	if len(raw) == 0 || string(raw) == "null" {
		return []qingLongScript{}, nil
	}

	var nodes []qingLongScriptNode
	if err := json.Unmarshal(raw, &nodes); err != nil {
		// 兼容「扁平字符串数组」与「树被包在 data/files 里」两种返回体
		var flat []string
		if flatErr := json.Unmarshal(raw, &flat); flatErr == nil {
			out := make([]qingLongScript, 0, len(flat))
			for _, item := range flat {
				if script := newQingLongScript(item); script.Path != "" {
					out = append(out, script)
				}
			}
			return out, nil
		}
		var page struct {
			Data  []qingLongScriptNode `json:"data"`
			Files []qingLongScriptNode `json:"files"`
		}
		if pageErr := json.Unmarshal(raw, &page); pageErr != nil {
			return nil, err
		}
		if page.Data != nil {
			nodes = page.Data
		} else {
			nodes = page.Files
		}
	}

	// 青龙新版本（>=2.x）的 /open/scripts/files 对「新建/空目录」经常返回空的
	// children，甚至目录节点没有 children 字段，导致老逻辑把目录误当成叶子文件丢弃。
	// 这里改为「主动递归」：对每个目录节点用 ?path=<dir> 单层拉取子内容，彻底绕开
	// children 为空的坑。目录节点一律用 type=="directory" 识别，路径以 key 为准。
	out := make([]qingLongScript, 0, len(nodes))
	d.collectScriptFilesRecursive(ctx, nodes, "", &out)
	return out, nil
}

// collectScriptFilesRecursive 递归展开脚本目录。
func (d *qingLongDriver) collectScriptFilesRecursive(ctx context.Context, nodes []qingLongScriptNode, parent string, out *[]qingLongScript) {
	for _, node := range nodes {
		path := scriptNodePath(node, parent)
		if isScriptDirNode(node) {
			children := node.Children
			if len(children) == 0 && path != "" {
				children = d.listScriptDir(ctx, path)
			}
			if len(children) > 0 {
				d.collectScriptFilesRecursive(ctx, children, path, out)
			}
			continue
		}
		if path != "" {
			*out = append(*out, newQingLongScript(path))
		}
	}
}

// isScriptDirNode 判断节点是不是目录：青龙新版用 type=="directory"；老版本靠非空
// children。二者任一成立都按目录处理，避免空目录/新目录被当成文件丢弃。
func isScriptDirNode(node qingLongScriptNode) bool {
	if node.Type == "directory" || node.Type == "dir" {
		return true
	}
	return len(node.Children) > 0
}

// listScriptDir 用 ?path= 单层拉取某个目录下的内容，拿不到就返回空。
func (d *qingLongDriver) listScriptDir(ctx context.Context, dir string) []qingLongScriptNode {
	var raw json.RawMessage
	query := "/open/scripts/files?path=" + url.QueryEscape(dir)
	if err := d.request(ctx, http.MethodGet, query, nil, &raw); err != nil {
		return nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var nodes []qingLongScriptNode
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return nil
	}
	return nodes
}

// scriptNodePath 从节点还原「相对脚本根目录的路径」。
func scriptNodePath(node qingLongScriptNode, parent string) string {
	raw := strings.TrimSpace(node.Value)
	if raw == "" {
		raw = strings.TrimSpace(node.Key)
	}
	if raw == "" {
		raw = strings.TrimSpace(node.Title)
	}
	raw = strings.Trim(strings.ReplaceAll(raw, `\`, "/"), "/")
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "/") {
		return raw
	}
	if own := strings.Trim(strings.ReplaceAll(strings.TrimSpace(node.Parent), `\`, "/"), "/"); own != "" {
		return own + "/" + raw
	}
	if parent != "" {
		return parent + "/" + raw
	}
	return raw
}
