package agent

// Run 的终态证据事务：本文件是"终态证据恰好一次且定序"这一聚合不变量的
// 唯一实现。Run 的 deferred 终结器与 EndAuditRun 共用 closeRunEvidence。

import (
	"context"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
)

// closeRunEvidence 执行 run 的终态证据事务定序：
//  1. 关闭 in-flight 模型调用（失败时对账一次）；
//  2. 补偿未决委派与工具审计；
//  3. 以因果错误（优先）追加终态事件。
//
// 因果与补偿错误编码为终态事件的失败原因；返回值仅表示终态事件是否成功
// 追加——失败时调用方据自身契约决定重试或交给启动恢复，且在此之前不得
// 释放任何进程状态。Run 与 EndAuditRun 共用此唯一实现。
func (r *Runtime) closeRunEvidence(ctx context.Context, scope *runExecution, cause error) error {
	ctx = context.WithoutCancel(ctx)
	if ledgerErr := r.callAuditor.closeActive(scope, runtimeError(cause)); ledgerErr != nil {
		if reconcileErr := r.callAuditor.reconcile(scope, ledgerErr); reconcileErr != nil && cause == nil {
			cause = reconcileErr
		}
	}
	if err := r.delegationAuditor.FailPending(ctx, scope); err != nil && cause == nil {
		cause = err
	}
	if err := r.toolAuditor.FailPending(ctx, scope); err != nil && cause == nil {
		cause = err
	}
	eventType, status := audit.EventRunCompleted, audit.StatusSucceeded
	if cause != nil {
		eventType, status = audit.EventRunFailed, audit.StatusFailed
	}
	return r.recordRunAudit(ctx, scope, eventType, status, cause)
}
