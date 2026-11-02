//////////////////////////////////////////////////////////////////////////
//
// OTN 告警上报(方案一)—— netconfd 进程入口
// T7:translib(alarm_app 随包 init 注册);T8:keysight 事件源 + 订阅管理。
//
//////////////////////////////////////////////////////////////////////////

package main

import (
	"flag"

	"github.com/Azure/sonic-mgmt-common/translib"
	log "github.com/golang/glog"
)

func main() {
	port := flag.Int("port", 830, "NETCONF over SSH listen port")
	redisAddr := flag.String("redis", "localhost:6379", "redis address for alarm keyspace watcher")
	flag.Parse()

	// T8 事件源:STATE_DB 读取器 + 订阅会话表 + keyspace watcher
	store, err := translib.NewCuralarmStore()
	if err != nil {
		log.Warning("netconfd: StateDB open failed, subscription disabled:", err)
		subMgr = nil
	} else {
		subMgr = NewSubManager(store)
		events := make(chan keyEvent, 64)
		go subMgr.Run(events)
		go StartKeywatcher(events, *redisAddr)
	}

	log.Info("netconfd starting on :", *port)
	if err := ServeSSH(*port); err != nil {
		log.Fatal("netconfd exited:", err)
	}
}
