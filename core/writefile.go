/*
	Copyright (C) CESS. All rights reserved.
	Copyright (C) Cumulus Encrypted Storage System. All rights reserved.

	SPDX-License-Identifier: Apache-2.0
*/

package core

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/CESSProject/p2p-go/pb"

	"github.com/gogo/protobuf/proto"
	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// pattern: /protocol-name/request-or-response-message/version
const writeFileRequest = "/file/writereq/v0"
const writeFileResponse = "/file/writeresp/v0"

type writeMsgResp struct {
	ch chan bool
	*pb.WritefileResponse
}

type WriteFileProtocol struct { // local host
	*Node
	requests map[string]*writeMsgResp // determine whether it is your own response
}

func (n *Node) NewWriteFileProtocol() *WriteFileProtocol {
	e := WriteFileProtocol{Node: n, requests: make(map[string]*writeMsgResp)}
	n.SetStreamHandler(writeFileRequest, e.onWriteFileRequest)
	n.SetStreamHandler(writeFileResponse, e.onWriteFileResponse)
	return &e
}

func (e *protocols) WriteFileAction(id peer.ID, roothash, path string) error {
	log.Printf("Will Sending writefileAction to: %s", id)
	var err error
	var ok bool
	var num int
	var offset int64
	var f *os.File

	// create message data
	req := &pb.WritefileRequest{
		MessageData: e.WriteFileProtocol.NewMessageData(uuid.New().String(), false),
		Roothash:    roothash,
	}

	req.Datahash, err = CalcPathSHA256(path)
	if err != nil {
		return err
	}

	f, err = os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// store request so response handler has access to it
	respChan := make(chan bool, 1)
	e.WriteFileProtocol.requests[req.MessageData.Id] = &writeMsgResp{
		ch: respChan,
	}
	defer delete(e.WriteFileProtocol.requests, req.MessageData.Id)
	defer close(respChan)

	timeout := time.NewTicker(P2PWriteReqRespTime)
	defer timeout.Stop()
	buf := make([]byte, FileProtocolBufSize)
	for {
		_, err = f.Seek(offset, 0)
		if err != nil {
			return err
		}
		num, err = f.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}

		if num == 0 {
			break
		}

		req.Data = buf[:num]
		req.Length = uint32(num)
		req.Offset = offset
		req.MessageData.Timestamp = time.Now().Unix()
		// calc signature
		req.MessageData.Sign = nil
		signature, err := e.WriteFileProtocol.SignProtoMessage(req)
		if err != nil {
			return err
		}

		// add the signature to the message
		req.MessageData.Sign = signature

		err = e.WriteFileProtocol.SendProtoMessage(id, writeFileRequest, req)
		if err != nil {
			return err
		}

		log.Printf("Writefile to: %s was sent. Msg Id: %s", id, req.MessageData.Id)

		// wait response
		timeout.Reset(P2PWriteReqRespTime)
		select {
		case ok = <-e.WriteFileProtocol.requests[req.MessageData.Id].ch:
			if !ok {
				return errors.New("Peer node response failure")
			}
		case <-timeout.C:
			return errors.New("Peer node response timed out")
		}

		if e.WriteFileProtocol.requests[req.MessageData.Id].WritefileResponse.Code == P2PResponseFinish {
			return nil
		}

		offset = e.WriteFileProtocol.requests[req.MessageData.Id].WritefileResponse.Offset
	}
	return nil
}

// remote peer requests handler
func (e *WriteFileProtocol) onWriteFileRequest(s network.Stream) {
	log.Printf("Recv writefileAction from: %s", s.Conn().RemotePeer().Pretty())
	// get request data
	data := &pb.WritefileRequest{}
	buf, err := io.ReadAll(s)
	if err != nil {
		s.Reset()
		log.Println(err)
		return
	}
	s.Close()

	// unmarshal it
	err = proto.Unmarshal(buf, data)
	if err != nil {
		log.Println(err)
		return
	}

	log.Printf("Received Writefile from %s. Roothash:%s Datahash:%s length:%d offset:%d",
		s.Conn().RemotePeer(), data.Roothash, data.Datahash, data.Length, data.Offset)

	valid := e.AuthenticateMessage(data, data.MessageData)
	if !valid {
		log.Println("Failed to authenticate message")
		return
	}

	log.Printf("Sending Writefile response to %s. Message id: %s", s.Conn().RemotePeer(), data.MessageData.Id)

	resp := &pb.WritefileResponse{
		MessageData: e.NewMessageData(data.MessageData.Id, false),
		Code:        P2PResponseOK,
		Offset:      0,
	}

	dir := filepath.Join(e.GetDirs().TmpDir, data.Roothash)
	fstat, err := os.Stat(dir)
	if err != nil {
		err = os.MkdirAll(dir, DirMode)
		if err != nil {
			return
		}
	} else {
		if !fstat.IsDir() {
			os.Remove(dir)
			err = os.MkdirAll(dir, DirMode)
			if err != nil {
				return
			}
		}
	}
	var size int64
	fpath := filepath.Join(dir, data.Datahash)
	fstat, err = os.Stat(fpath)
	if err == nil {
		size = fstat.Size()
		if size >= FragmentSize {
			time.Sleep(time.Second * 5)
			if size > FragmentSize {
				os.Remove(fpath)
			} else {
				hash, err := CalcPathSHA256(fpath)
				if err != nil || hash != data.Datahash {
					os.Remove(fpath)
				} else {
					resp.Code = P2PResponseFinish
				}
			}
			// sign the data
			signature, err := e.SignProtoMessage(resp)
			if err != nil {
				log.Println("failed to sign response")
				return
			}
			// add the signature to the message
			resp.MessageData.Sign = signature
			err = e.SendProtoMessage(s.Conn().RemotePeer(), writeFileResponse, resp)
			if err != nil {
				log.Printf("Writefile response to %s sent failed.", s.Conn().RemotePeer().String())
			}
			return
		}
	}

	f, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, os.ModePerm)
	if err != nil {
		log.Println("OpenFile err:", err)
		return
	}
	defer f.Close()
	fstat, err = f.Stat()
	if err != nil {
		return
	}
	size = fstat.Size()

	_, err = f.Write(data.Data[:data.Length])
	if err != nil {
		log.Println("Write err:", err)
		return
	}

	if int(int(size)+int(data.Length)) == FragmentSize {
		hash, err := CalcPathSHA256(fpath)
		if err != nil || hash != data.Datahash {
			os.Remove(fpath)
		} else {
			resp.Code = P2PResponseFinish
		}
	} else {
		resp.Offset = size + int64(data.Length)
	}
	// sign the data
	signature, err := e.SignProtoMessage(resp)
	if err != nil {
		log.Println("failed to sign response")
		return
	}

	// add the signature to the message
	resp.MessageData.Sign = signature

	// send response to the request using the message string he provided
	err = e.SendProtoMessage(s.Conn().RemotePeer(), writeFileResponse, resp)
	if err != nil {
		log.Printf("Writefile response to %s sent failed.", s.Conn().RemotePeer().String())
	}
}

// remote peer response handler
func (e *WriteFileProtocol) onWriteFileResponse(s network.Stream) {
	data := &pb.WritefileResponse{}
	buf, err := io.ReadAll(s)
	if err != nil {
		s.Reset()
		log.Println(err)
		return
	}
	s.Close()

	// unmarshal it
	err = proto.Unmarshal(buf, data)
	if err != nil {
		log.Println(err)
		return
	}

	// authenticate message content
	valid := e.AuthenticateMessage(data, data.MessageData)

	if !valid {
		log.Println("Failed to authenticate message")
		return
	}

	// locate request data and remove it if found
	_, ok := e.requests[data.MessageData.Id]
	if ok {
		if data.Code == P2PResponseOK || data.Code == P2PResponseFinish {
			e.requests[data.MessageData.Id].ch <- true
			e.requests[data.MessageData.Id].WritefileResponse = data
		} else {
			e.requests[data.MessageData.Id].ch <- false
		}
	} else {
		log.Println("Failed to locate request data boject for response")
		return
	}

	log.Printf("Received Writefile response from %s. Message id:%s. Code: %d Offset:%d.",
		s.Conn().RemotePeer(), data.MessageData.Id, data.Code, data.Offset)
}