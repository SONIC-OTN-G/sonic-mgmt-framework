//////////////////////////////////////////////////////////////////////////
//
// OTN 告警上报(方案一)—— netconfd 单测
// 覆盖:hello/capabilities、get-config 占位、未知操作 rpc-error、close-session
//
//////////////////////////////////////////////////////////////////////////

package main

import (
	"strings"
	"testing"
)

func newTestSession() *Session {
	return &Session{ID: 1, Write: func([]byte) error { return nil }}
}

func TestHandleHello(t *testing.T) {
	s := newTestSession()
	resp, closeConn, err := HandleRPC(s, []byte(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities></hello>`))
	if err != nil || closeConn {
		t.Fatalf("err=%v closeConn=%v", err, closeConn)
	}
	r := string(resp)
	for _, want := range []string{capBase10, capNotification,
		"http://openconfig.net/yang/alarms?module=openconfig-alarms&amp;revision=2019-07-09",
		"<session-id>1</session-id>"} {
		if !strings.Contains(r, want) {
			t.Fatalf("capabilities missing %q in %s", want, r)
		}
	}
}

func TestHandleGetConfigPlaceholder(t *testing.T) {
	s := newTestSession()
	orig := getAlarmsData
	defer func() { getAlarmsData = orig }()
	getAlarmsData = func() ([]byte, error) {
		return alarmsJSONtoXML([]byte(`{"openconfig-alarms:alarms":{"alarm":[{"resource":"linecard1","alarm-type-id":"LOW_TEMPERATURE_ALARM","severity":"WARNING","text":"t","time-created":"2026-10-24T11:59:20Z"}]}}`))
	}
	resp, closeConn, err := HandleRPC(s, []byte(`<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="101"><get-config><source><running/></source></get-config></rpc>`))
	if err != nil || closeConn {
		t.Fatalf("err=%v closeConn=%v", err, closeConn)
	}
	r := string(resp)
	for _, want := range []string{`message-id="101"`, "<data>", `<alarms xmlns="http://openconfig.net/yang/alarms">`, "<id>linecard1#LOW_TEMPERATURE_ALARM</id>", "<time-created>", "</state>"} {
		if !strings.Contains(r, want) {
			t.Fatalf("missing %q in %s", want, r)
		}
	}
}

func TestHandleUnknownOperation(t *testing.T) {
	s := newTestSession()
	resp, _, err := HandleRPC(s, []byte(`<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="7"><validate/></rpc>`))
	if err != nil {
		t.Fatal(err)
	}
	r := string(resp)
	if !strings.Contains(r, "<rpc-error>") || !strings.Contains(r, "operation-not-supported") || !strings.Contains(r, `message-id="7"`) {
		t.Fatalf("bad rpc-error: %s", r)
	}
}

func TestHandleCloseSession(t *testing.T) {
	s := newTestSession()
	resp, closeConn, err := HandleRPC(s, []byte(`<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="9"><close-session/></rpc>`))
	if err != nil || !closeConn {
		t.Fatalf("err=%v closeConn=%v", err, closeConn)
	}
	if !strings.Contains(string(resp), "<ok/>") {
		t.Fatalf("want ok: %s", resp)
	}
}
