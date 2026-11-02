//////////////////////////////////////////////////////////////////////////
//
// OTN 告警上报(方案一)—— 订阅会话表与 notification 推送(RFC 5277)
// 事件源:redis keyspace(__keyspace@6__:CURALARM*)hset/del;
// 收到事件重扫全表(alarm.GetAlarmEntries)与缓存对比:
//   新键 → RAISED(全字段);消失键 → CLEARED(缓存补全字段)。
// 每会话独立推送,写失败即摘除,不拖垮其他会话。
//
//////////////////////////////////////////////////////////////////////////

package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Azure/sonic-mgmt-common/translib/alarm"
	log "github.com/golang/glog"
	"github.com/redis/go-redis/v9"
)

// keyEvent 一次 keyspace 变化
type keyEvent struct {
	op  string // hset / hdel / del / unlink
	key string // 裸键名 resource#type-id(不含 CURALARM| 前缀)
}

// SubManager 订阅会话表 + 事件→通知扇出
type SubManager struct {
	mu       sync.Mutex
	sessions map[uint32]*Session
	cache    map[string]map[string]interface{} // key → entry(translate 输出)
	store    alarm.CuralarmStore
}

func NewSubManager(store alarm.CuralarmStore) *SubManager {
	return &SubManager{
		sessions: map[uint32]*Session{},
		cache:    map[string]map[string]interface{}{},
		store:    store,
	}
}

func (m *SubManager) AddSession(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s
	log.Infof("subscribe: session %d added (total %d)", s.ID, len(m.sessions))
}

func (m *SubManager) RemoveSession(id uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}

// Run 消费事件直至通道关闭
func (m *SubManager) Run(events <-chan keyEvent) {
	for ev := range events {
		log.Infof("subscribe: event op=%s key=%s", ev.op, ev.key)
		m.onEvent()
	}
}

// onEvent 重扫全表 → 与缓存 diff → 推送 RAISED/CLEARED
func (m *SubManager) onEvent() {
	log.Info("subscribe: rescan begin")
	entries, err := alarm.GetAlarmEntries(m.store)
	if err != nil {
		log.Warning("subscribe: rescan failed:", err)
		return
	}
	log.Infof("subscribe: rescan done, %d entries", len(entries))
	now := map[string]map[string]interface{}{}
	for _, e := range entries {
		key := e["resource"].(string) + "#" + e["alarm-type-id"].(string)
		now[key] = e
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for key, e := range now {
		if _, was := m.cache[key]; !was {
			m.push(key, e, "RAISED")
		}
	}
	for key, e := range m.cache {
		if _, is := now[key]; !is {
			m.push(key, e, "CLEARED")
		}
	}
	m.cache = now
}

// push 组通知帧并扇出;写失败的会话立即摘除
func (m *SubManager) push(key string, e map[string]interface{}, status string) {
	frame, err := notificationXML(e, status)
	if err != nil {
		log.Warning("subscribe: notification build failed:", err)
		return
	}
	for id, s := range m.sessions {
		if err := s.Write(frame); err != nil {
			log.Warningf("subscribe: session %d write failed, removing", id)
			delete(m.sessions, id)
		}
	}
}

// notificationXML 单条告警的 RFC 5277 通知帧(status 仅通知携带)
func notificationXML(e map[string]interface{}, status string) ([]byte, error) {
	res, _ := e["resource"].(string)
	typeID, _ := e["alarm-type-id"].(string)
	sev, _ := e["severity"].(string)
	text, _ := e["text"].(string)
	tc, _ := e["time-created"].(string)
	ns := tc
	if t, err := time.Parse(time.RFC3339, tc); err == nil {
		ns = fmt.Sprintf("%d", t.UnixNano())
	}
	frag := strings.Replace(alarmFrag(res, typeID, sev, text, ns),
		"</state>", "<status>"+esc(status)+"</status></state>", 1)
	return []byte(fmt.Sprintf(
		`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>%s</eventTime>%s</notification>`,
		esc(time.Now().UTC().Format(time.RFC3339)), frag)), nil
}

// StartKeywatcher 订阅 redis keyspace(阻塞,放 goroutine)
// 注意:glob 模式必须用 PSubscribe(SUBSCRIBE 是精确频道名)
func StartKeywatcher(events chan<- keyEvent, redisAddr string) {
	rc := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer rc.Close()
	ctx := context.Background()
	sub := rc.PSubscribe(ctx, "__keyspace@6__:CURALARM*")
	defer sub.Close()

	log.Info("keywatcher: psubscribed __keyspace@6__:CURALARM* @", redisAddr)
	for {
		msg, err := sub.Receive(ctx)
		if err != nil {
			log.Warning("keywatcher: receive failed:", err)
			return
		}
		m, ok := msg.(*redis.Message)
		if !ok {
			continue
		}
		// 频道 __keyspace@6__:CURALARM|<key>;Payload = 操作名
		key := strings.TrimPrefix(m.Channel, "__keyspace@6__:CURALARM|")
		switch m.Payload {
		case "hset", "hdel", "del", "unlink":
			select {
			case events <- keyEvent{op: m.Payload, key: key}:
			default:
				log.Warning("keywatcher: event queue full, dropping", m.Channel)
			}
		}
	}
}
