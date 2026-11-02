//////////////////////////////////////////////////////////////////////////
//
// OTN 告警上报(方案一)—— netconfd SSH 传输层
// RFC 6242:SSH subsystem "netconf";演示级密码认证;
// 帧格式 base:1.0(]]>]]>),读一帧 → HandleRPC → 回一帧。
//
//////////////////////////////////////////////////////////////////////////

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"

	log "github.com/golang/glog"
	"golang.org/x/crypto/ssh"
)

// SessionID 分配器
var sessionID atomic.Uint32

// ServeSSH 起 :830 SSH 服务并阻塞
func ServeSSH(port int) error {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			user := conn.User()
			want := os.Getenv("NETCONF_PASSWORD")
			if want == "" {
				want = "YourPaSsWoRd"
			}
			if user == "admin" && string(password) == want {
				log.Info("netconfd: auth ok for", user)
				return &ssh.Permissions{}, nil
			}
			log.Warning("netconfd: auth failed for", user)
			return nil, fmt.Errorf("auth failed")
		},
	}

	signer, err := demoHostKey()
	if err != nil {
		return err
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	log.Info("netconfd: listening on :", port)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go handleConn(conn, cfg)
	}
}

// demoHostKey:演示级,进程内生成临时 host key(网管侧 hostkey_verify=false)
func demoHostKey() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	// x/crypto 2020 版无 MarshalPrivateKey;NewSignerFromKey 直接吃私钥
	return ssh.NewSignerFromKey(priv)
}

// handleConn:SSH 握手 → 等 "netconf" subsystem 通道 → 进会话循环
func handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		log.Warning("netconfd: handshake failed:", err)
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, chReq, _ := newChan.Accept()
		go func() {
			for req := range chReq {
				if req.WantReply {
					isNetconfSubsys := req.Type == "subsystem" &&
						strings.Contains(string(req.Payload[:minInt(16, len(req.Payload))]), "netconf")
					req.Reply(isNetconfSubsys, nil)
				}
			}
		}()
		go netconfSession(ch)
	}
}

// netconfSession:一个 NETCONF 会话的生命周期
func netconfSession(ch ssh.Channel) {
	s := &Session{
		ID:    sessionID.Add(1),
		Write: func(b []byte) error { return sendDoc(ch, b) },
	}

	// 服务端 hello 先行
	if err := sendDoc(ch, buildHello(s.ID)); err != nil {
		log.Warning("netconfd: send hello failed:", err)
		return
	}

	for {
		doc, err := readDoc(ch)
		if err != nil {
			return // EOF/断开,正常结束
		}
		resp, closeConn, err := HandleRPC(s, doc)
		if err != nil {
			log.Warning("netconfd: rpc error:", err)
			return
		}
		if resp != nil {
			if err := sendDoc(ch, resp); err != nil {
				return
			}
		}
		if closeConn {
			ch.Close()
			return
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// readDoc:读到 "]]>]]>" 为一帧(RFC 6242 base 1.0)
func readDoc(r io.Reader) ([]byte, error) {
	var buf strings.Builder
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			s := buf.String()
			if idx := strings.Index(s, framesep); idx >= 0 {
				out := strings.TrimSpace(s[:idx])
				// 简化处理:base1.0 下客户端一问一答,无黏包场景
				return []byte(out), nil
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

// sendDoc:发一帧 + 分隔符
func sendDoc(w io.Writer, doc []byte) error {
	_, err := w.Write(append(append([]byte{}, doc...), []byte("\n"+framesep+"\n")...))
	return err
}
