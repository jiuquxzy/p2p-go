/*
	Copyright (C) CESS. All rights reserved.
	Copyright (C) Cumulus Encrypted Storage System. All rights reserved.

	SPDX-License-Identifier: Apache-2.0
*/

package core

import (
	"github.com/libp2p/go-libp2p/core/peer"
)

type Protocol interface {
	WriteFileAction(id peer.ID, roothash, path string) error
	ReadFileAction(id peer.ID, roothash, datahash, path string, size int64) error
	ReadDataAction(id peer.ID, roothash, datahash, path string, size int64) error
	ReadDataStatAction(id peer.ID, roothash string, datahash string) (uint64, error)
	OnlineAction(id peer.ID) error
}

type protocols struct {
	ProtocolPrefix string
	*WriteFileProtocol
	*ReadFileProtocol
	*ReadDataProtocol
	*ReadDataStatProtocol
	*OnlineProtocol
}

func NewProtocol() *protocols {
	return &protocols{}
}

func (p *protocols) SetProtocolPrefix(protocolPrefix string) {
	p.ProtocolPrefix = protocolPrefix
}
