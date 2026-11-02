//////////////////////////////////////////////////////////////////////////
//
// OTN 告警上报(方案一)—— netconfd RPC 殼
// 只管协议封包和会话,不碰告警内容;告警语义在 translib alarm_app。
// 支持 RFC 6241 子集:hello/capabilities、get、get-config、
// create-subscription(Task 8)、close/kill-session;
// 帧格式仅 base:1.0(]]>]]>), capabilities 故意只声明 base:1.0。
//
//////////////////////////////////////////////////////////////////////////

package main

import (
	"strings"
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
)

const (
	framesep = "]]>]]>"
	// capabilities:只声明 base:1.0(简化帧格式);notification 能力供订阅
	capBase10          = "urn:ietf:params:netconf:base:1.0"
	capNotification    = "urn:ietf:params:netconf:capability:notification:1.0"
	capOpenconfigAlarm = "http://openconfig.net/yang/alarms?module=openconfig-alarms&revision=2019-07-09"
)

// subMgr 订阅管理器(main 初始化;单测可替换)
var subMgr *SubManager

// Session 是一条 NETCONF 会话;Write 供 notification 推送复用(Task 8)
type Session struct {
	ID     uint32
	Write  func([]byte) error
	closed bool
}

// helloMsg 解析客户端 hello;发送服务端 hello 用 buildHello
type helloMsg struct {
	XMLName     xml.Name `xml:"hello"`
	Caps        []string `xml:"capabilities>capability"`
}

type getReq struct{}
type getConfigReq struct{}
type killSessionReq struct {
	SessionID uint32 `xml:"session-id"`
}

// rpcMsg 是 <rpc> 信封;message-id 必须原样带回 rpc-reply
type rpcMsg struct {
	XMLName            xml.Name        `xml:"rpc"`
	MsgID              string          `xml:"message-id,attr"`
	Get                *getReq         `xml:"get"`
	GetConfig          *getConfigReq   `xml:"get-config"`
	CloseSession       *struct{}       `xml:"close-session"`
	KillSession        *killSessionReq `xml:"kill-session"`
	CreateSubscription *struct{}       `xml:"create-subscription"`
}

// buildHello 服务端 hello: capabilities + session-id
func buildHello(sessionID uint32) []byte {
	var b bytes.Buffer
	b.WriteString(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities>`)
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	for _, c := range []string{capBase10, capNotification, capOpenconfigAlarm} {
		b.WriteString("<capability>" + esc.Replace(c) + "</capability>")
	}
	b.WriteString("</capabilities><session-id>" + strconv.FormatUint(uint64(sessionID), 10) + "</session-id></hello>")
	return b.Bytes()
}

// HandleRPC 处理一帧文档;返回 (响应, 是否关闭会话, 错误)
// 返回 nil 响应表示无需回包。
func HandleRPC(s *Session, doc []byte) (resp []byte, closeConn bool, err error) {
	var hello helloMsg
	if err := xml.Unmarshal(doc, &hello); err == nil && hello.XMLName.Local == "hello" {
		// 客户端 hello:回服务端 hello
		return buildHello(s.ID), false, nil
	}

	var rpc rpcMsg
	if err := xml.Unmarshal(doc, &rpc); err != nil || rpc.XMLName.Local != "rpc" {
		return rpcErrorReply("", "malformed-message", "not a NETCONF rpc or hello"), false, nil
	}

	switch {
	case rpc.Get != nil:
		fallthrough
	case rpc.GetConfig != nil:
		// Task 7:translib alarm_app → 模型 XML
		xmlBody, err := getAlarmsData()
		if err != nil {
			return rpcErrorReply(rpc.MsgID, "operation-failed", err.Error()), false, nil
		}
		return rpcDataReply(rpc.MsgID, "<data>"+string(xmlBody)+"</data>"), false, nil

	case rpc.CloseSession != nil:
		return rpcOKReply(rpc.MsgID), true, nil

	case rpc.KillSession != nil:
		return rpcOKReply(rpc.MsgID), false, nil

	case rpc.CreateSubscription != nil:
		// T8:注册会话,后续 keyspace 事件以 <notification> 推送
		if subMgr == nil {
			return rpcErrorReply(rpc.MsgID, "operation-failed", "subscription manager not ready"), false, nil
		}
		subMgr.AddSession(s)
		return rpcOKReply(rpc.MsgID), false, nil

	default:
		return rpcErrorReply(rpc.MsgID, "operation-not-supported", "unsupported operation"), false, nil
	}
}

func rpcOKReply(msgID string) []byte {
	return []byte(fmt.Sprintf(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="%s"><ok/></rpc-reply>`, msgID))
}

func rpcDataReply(msgID, data string) []byte {
	return []byte(fmt.Sprintf(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="%s">%s</rpc-reply>`, msgID, data))
}

func rpcErrorReply(msgID, errType, msg string) []byte {
	return []byte(fmt.Sprintf(
		`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="%s"><rpc-error><error-type>protocol</error-type><error-tag>%s</error-tag><error-severity>error</error-severity><error-message>%s</error-message></rpc-error></rpc-reply>`,
		msgID, errType, msg))
}
