# 一个服务端进程只管一张牌桌，Table Code 就是 socket 文件名

`poker serve --blinds 1/2` 启动时生成一个 Table Code 并打印出来，监听 `~/.poker/<code>.sock`。`poker join ABC123` 把 code 拼进路径就找到了它。不提供 `table create` / `list` / `close`，一个进程不管多张桌。

同机场景下多开几个 serve 进程的成本近乎为零，而多桌管理会立刻引入牌桌列表、生命周期与空桌回收这一整套与目标无关的东西。让 Table Code 直接充当 socket 文件名，则把「按房间号加入」这个需求降解成一次文件路径拼接——不需要注册表，不需要服务发现，也不需要广播。
