# 牌局规则是纯核心，依赖方向单向朝内

目录按技术分层：`cmd/poker/`、`internal/server/`、`internal/client/`、`internal/protocol/`、`internal/poker/`。其中 `internal/poker/` 存放全部牌局规则——发牌、下注合法性、Street 推进、Side Pot 计算、牌力比较——并且**不含任何 IO、goroutine 或 `time.Now()`**：它接收当前状态与一个 Action，返回新状态与一串事件；时间与随机源一律作为参数注入。它不 import 任何其他内部包。

这条约束不是风格偏好，是另外两个决策的前提：没有可注入的随机源，ADR-0004 的 `--seed` 就无法让边池分配这类逻辑可复现测试；核心一旦混进 socket 或终端渲染，ADR-0006 的可见性矩阵就再也无法用纯内存的毫秒级测试逐格守住。这是整个项目里最容易被悄悄侵蚀的约束。
