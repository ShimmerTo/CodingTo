package connection

import (
	// 驱动注册：仅作 side-effect import，bridge 二进制独占这些依赖。
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// 本文件只做驱动注册；驱动名称与 dbsecurity.ConnectionConfig.DSN 的
// 返回值保持一致（mysql / postgres / sqlite）。
