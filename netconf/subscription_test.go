//////////////////////////////////////////////////////////////////////////
//
// OTN 告警上报(方案一)—— 订阅推送单测(fake store/会话,免 redis)
// 覆盖:RAISED/CLEARED 判定与缓存补全、双会话扇出、写失败摘除
//
//////////////////////////////////////////////////////////////////////////

package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fake store(可变条目,模拟 raise/clear)
type subFakeStore struct {
	mu      sync.Mutex
	entries map[string]map[string]string
}

func (f *subFakeStore) Keys() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.entries))
	for k := range f.entries {
		out = append(out, k)
	}
	return out, nil
}

func (f *subFakeStore) HGetAll(key string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entries[key], nil
}

func (f *subFakeStore) raise(key, text string, ts int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, tid := splitKey(key)
	f.entries[key] = map[string]string{
		"id": key, "time-created": fmt.Sprintf("%d", ts),
		"text": text, "severity": "WARNING", "type-id": tid, "resource": res,
	}
}

func (f *subFakeStore) clear(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, key)
}

func splitKey(key string) (res, tid string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '#' {
			return key[:i], key[i+1:]
		}
	}
	return "", key
}

// fake 会话:Write 收集帧;可注入错误
type fakeSession struct {
	id     uint32
	mu     sync.Mutex
	frames []string
	err    error // 非 nil 时 Write 返回错误
}

func (f *fakeSession) toSession() *Session {
	return &Session{ID: f.id, Write: f.write}
}

func (f *fakeSession) write(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.frames = append(f.frames, string(b))
	return nil
}

func (f *fakeSession) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.frames)
}

func newTestMgr() (*SubManager, *subFakeStore) {
	st := &subFakeStore{entries: map[string]map[string]string{}}
	return NewSubManager(st), st
}

func TestSubRaiseThenClear(t *testing.T) {
	mgr, st := newTestMgr()
	fs := &fakeSession{id: 1}
	mgr.AddSession(fs.toSession())

	// raise
	st.raise("linecard1#LOW_TEMPERATURE_ALARM", "sub raise", 1792923124535673153)
	mgr.onEvent()
	if fs.count() != 1 {
		t.Fatalf("want 1 notification, got %d", fs.count())
	}
	f1 := fs.frames[0]
	for _, want := range []string{
		"<notification xmlns=\"urn:ietf:params:xml:ns:netconf:notification:1.0\"",
		"<eventTime>", "<status>RAISED</status>",
		"<text>sub raise</text>", "<severity>WARNING</severity>",
		"<id>linecard1#LOW_TEMPERATURE_ALARM</id>",
	} {
		if !strings.Contains(f1, want) {
			t.Fatalf("raise frame missing %q in %s", want, f1)
		}
	}

	// clear:缓存补全字段(text 仍在,只是条目消失)
	st.clear("linecard1#LOW_TEMPERATURE_ALARM")
	mgr.onEvent()
	if fs.count() != 2 {
		t.Fatalf("want 2 notifications, got %d", fs.count())
	}
	f2 := fs.frames[1]
	if !strings.Contains(f2, "<status>CLEARED</status>") || !strings.Contains(f2, "<text>sub raise</text>") {
		t.Fatalf("clear frame broken: %s", f2)
	}

	// 无第三次(缓存已同步,无事件)
	mgr.onEvent()
	if fs.count() != 2 {
		t.Fatalf("unexpected extra notification: %s", fs.frames[2])
	}
}

func TestSubTwoSessionsAndRemoval(t *testing.T) {
	mgr, st := newTestMgr()
	good := &fakeSession{id: 1}
	bad := &fakeSession{id: 2, err: fmt.Errorf("broken pipe")}
	mgr.AddSession(good.toSession())
	mgr.AddSession(bad.toSession())

	st.raise("linecard1#A", "first", 1792923124535673153)
	mgr.onEvent()
	if good.count() != 1 || bad.count() != 0 {
		t.Fatalf("good=%d bad=%d(坏会话不该收到)", good.count(), bad.count())
	}
	// 第二次事件:坏会话已摘除,只有 good 收
	st.raise("linecard1#B", "second", 1792923124535673154)
	mgr.onEvent()
	if good.count() != 2 {
		t.Fatalf("good should receive 2, got %d", good.count())
	}
}

func TestHandleCreateSubscription(t *testing.T) {
	mgr, _ := newTestMgr()
	old := subMgr
	subMgr = mgr
	defer func() { subMgr = old }()

	s := &Session{ID: 7, Write: func([]byte) error { return nil }}
	resp, closeConn, err := HandleRPC(s, []byte(`<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="20"><create-subscription xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><stream><name>openconfig-alarms</name></stream></create-subscription></rpc>`))
	if err != nil || closeConn {
		t.Fatalf("err=%v closeConn=%v", err, closeConn)
	}
	if !strings.Contains(string(resp), "<ok/>") {
		t.Fatalf("want ok: %s", resp)
	}
	mgr.mu.Lock()
	_, registered := mgr.sessions[7]
	mgr.mu.Unlock()
	if !registered {
		t.Fatal("session 7 not registered")
	}
}

func TestNotificationXML(t *testing.T) {
	e := map[string]interface{}{
		"resource": "linecard1", "alarm-type-id": "X", "severity": "MAJOR",
		"text": "a<b&c", "time-created": "2026-10-24T11:59:20Z",
	}
	f, err := notificationXML(e, "RAISED")
	if err != nil {
		t.Fatal(err)
	}
	s := string(f)
	// 特殊字符转义
	if !strings.Contains(s, "a&lt;b&amp;c") {
		t.Fatalf("text not escaped: %s", s)
	}
	// time-created 转回 ns 整数
	if !strings.Contains(s, "<time-created>1792843160000000000</time-created>") {
		t.Fatalf("time-created not ns: %s", s)
	}
}
