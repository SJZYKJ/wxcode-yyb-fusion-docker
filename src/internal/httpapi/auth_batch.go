package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"yyb_go/internal/auth"
)

// ---------- 用户批量删除（/users 页面「删除所选」） ----------
//
// 单独一个文件，与 auth.go 里的单用户 handler 分开：
//   - 批量删除是独立的一组校验规则（自己、最后一个管理员、上限、skipped），
//     和单行删除的语义并不相同，混在一起容易改错；
//   - 另一个成员正在同时修改 auth.go，物理隔离可以把合并冲突降到零。
//
// 注册位置见 app.go：必须挂在 router.Use(a.requireAdminSession()) 之后，
// 这样非管理员会在中间件层就被 403 挡掉，而不是进到 handler 里才判断。
const batchDeletePath = "/api/auth/users/batch-delete"

// batchDeleteMaxIDs 直接复用 store 层的上限常量，避免两处各写一个 200 而漂移。
const batchDeleteMaxIDs = auth.BatchDeleteMaxIDs

// batchDeleteRequest 是 POST /api/auth/users/batch-delete 的请求体。
// 用 *[]int64 区分「字段缺失」与「显式空数组」：两者都要报「未选择要删除的
// 用户」，但缺失时属于请求格式问题，用指针能保证判定不被 JSON 零值吞掉。
type batchDeleteRequest struct {
	IDs *[]int64 `json:"ids"`
}

// handleUsersBatchDelete 批量删除所选用户。
//
// 成功返回 {"deleted":[id...],"skipped":[{"id":n,"reason":"..."}],"count":N}；
// 任何一条硬性规则（含自己、删空管理员、空 ids、超上限）被触发时整个请求 400，
// 且一条都不会被删除——原子性由 auth.Store.BatchDeleteUsers 的事务保证。
func (a *App) handleUsersBatchDelete(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "当前实例未启用多用户鉴权")
		return
	}
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor, _ := currentAuth(r)
	if actor == nil {
		writeError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body batchDeleteRequest
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if body.IDs == nil || len(*body.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "未选择要删除的用户")
		return
	}
	ids := *body.IDs
	if len(ids) > auth.BatchDeleteMaxIDs {
		writeError(w, http.StatusBadRequest, "单次最多删除 "+strconv.Itoa(auth.BatchDeleteMaxIDs)+" 个用户")
		return
	}
	// 提前挡住自己：store 里也有同样的判断，但在 API 边界再挡一次可以让错误
	// 信息稳定，同时保证「整个请求不生效」在下游被调用之前就已确定。
	for _, id := range ids {
		if id == actor.ID {
			writeError(w, http.StatusBadRequest, "不能删除当前登录用户")
			return
		}
	}
	deleted, skipped, err := a.auth.BatchDeleteUsers(r.Context(), actor.ID, ids)
	if err != nil {
		// 校验规则（自己 / 最后一个管理员 / 空 ids / 超上限）统一走 400，
		// 与单行删除的 handleUserAction 保持一致；数据库层的错误才落到 500。
		status := http.StatusBadRequest
		if !isBatchDeleteValidationError(err) {
			status = http.StatusInternalServerError
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": deleted,
		"skipped": batchDeleteSkippedPayload(skipped),
		"count":   len(deleted),
	})
}

// batchDeleteValidationMessages 列出可以安全地向客户端回显 400 的错误片段。
// 其余错误（数据库不可用等）一律 500，避免把内部细节当成参数错误暴露出去。
var batchDeleteValidationMessages = []string{
	"未选择要删除的用户",
	"不能删除当前登录用户",
	"至少需要保留 1 个管理员",
	"单次最多删除",
	"当前登录用户不存在",
	"当前登录用户已被删除",
	"需要管理员权限",
}

func isBatchDeleteValidationError(err error) bool {
	message := err.Error()
	for _, fragment := range batchDeleteValidationMessages {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

// batchDeleteSkippedPayload 把 store 层的跳过记录转成对外 JSON。
//
// JSON key 固定为 id / reason（需求约定的形状）。这里用显式 map 而不是给
// auth.BatchDeleteSkip 再加 tag 的原因：跳过项本来就是「给前端看的展示数据」，
// 在 API 边界做一次显式映射，比让 store 结构体的 tag 兼任对外契约更清楚，
// 也避免 store 结构体改字段名时悄悄改掉接口形状。
func batchDeleteSkippedPayload(skipped []auth.BatchDeleteSkip) []map[string]any {
	out := make([]map[string]any, 0, len(skipped))
	for _, item := range skipped {
		out = append(out, map[string]any{"id": item.ID, "reason": item.SkipReason})
	}
	return out
}
