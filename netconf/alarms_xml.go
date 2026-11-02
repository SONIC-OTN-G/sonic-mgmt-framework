//////////////////////////////////////////////////////////////////////////
//
// OTN 告警上报(方案一)—— alarm_app JSON → openconfig-alarms XML 组装
// 模型形态:/alarms/alarm{id}/state/{id,resource,text,time-created,
// severity,type-id};time-created 在模型里是 oc-types:timeticks64
// (ns 整数),app JSON 里的 RFC3339 在此转回 ns。
//
//////////////////////////////////////////////////////////////////////////

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Azure/sonic-mgmt-common/translib"
)

// netconfd 侧可替换的取数函数(单测 override;生产 = translib.Get + JSON→XML)
var getAlarmsData = func() ([]byte, error) {
	resp, err := translib.Get(translib.GetRequest{
		Path: "/openconfig-alarms:alarms",
		User: translib.UserRoles{},
	})
	if err != nil {
		return nil, err
	}
	return alarmsJSONtoXML(resp.Payload)
}

type alarmJSONEntry struct {
	Resource    string `json:"resource"`
	AlarmTypeID string `json:"alarm-type-id"`
	Severity    string `json:"severity"`
	Text        string `json:"text"`
	TimeCreated string `json:"time-created"` // RFC3339
}

// esc XML 文本转义
func esc(s string) string {
	r := bytes.NewBuffer(nil)
	for _, c := range s {
		switch c {
		case '&':
			r.WriteString("&amp;")
		case '<':
			r.WriteString("&lt;")
		case '>':
			r.WriteString("&gt;")
		default:
			r.WriteRune(c)
		}
	}
	return r.String()
}

// alarmFrag 单条 <alarm> 片段(不含 status;status 仅通知帧携带)
func alarmFrag(res, typeID, sev, text, timeCreatedNS string) string {
	id := res + "#" + typeID
	return fmt.Sprintf(
		`<alarm><id>%s</id><state><id>%s</id><resource>%s</resource><text>%s</text><time-created>%s</time-created><severity>%s</severity><type-id>%s</type-id>%s</state></alarm>`,
		esc(id), esc(id), esc(res), esc(text), esc(timeCreatedNS), esc(sev), esc(typeID), "")
}

// alarmsJSONtoXML 把 alarm_app 的 JSON payload 组装成模型 XML
//(<alarms>...</alarms>,不含 <data> 信封)
func alarmsJSONtoXML(payload []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, fmt.Errorf("alarms payload not json: %w", err)
	}
	body, ok := root["openconfig-alarms:alarms"]
	if !ok {
		// 空告警表:输出空容器
		return []byte(`<alarms xmlns="http://openconfig.net/yang/alarms"/>`), nil
	}
	var list struct {
		Alarm []alarmJSONEntry `json:"alarm"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("alarms list not json: %w", err)
	}

	var b bytes.Buffer
	b.WriteString(`<alarms xmlns="http://openconfig.net/yang/alarms">`)
	for _, a := range list.Alarm {
		ns := a.TimeCreated
		if t, err := time.Parse(time.RFC3339, a.TimeCreated); err == nil {
			ns = fmt.Sprintf("%d", t.UnixNano())
		}
		b.WriteString(alarmFrag(a.Resource, a.AlarmTypeID, a.Severity, a.Text, ns))
	}
	b.WriteString(`</alarms>`)
	return b.Bytes(), nil
}
