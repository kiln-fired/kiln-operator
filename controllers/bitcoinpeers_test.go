package controllers

import (
	"encoding/json"
	"fmt"

	"github.com/btcsuite/btcd/rpcclient"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type fakeBitcoinPeerRPC struct {
	persistent []string
	calls      []string
	failAdd    string
	failRemove string
}

func (f *fakeBitcoinPeerRPC) RawRequest(method string, params []json.RawMessage) (json.RawMessage, error) {
	Expect(method).To(Equal("getaddednodeinfo"))
	Expect(params).To(Equal([]json.RawMessage{json.RawMessage("false")}))
	return json.Marshal(f.persistent)
}

func (f *fakeBitcoinPeerRPC) AddNode(host string, command rpcclient.AddNodeCommand) error {
	f.calls = append(f.calls, string(command)+":"+host)
	switch command {
	case rpcclient.ANAdd:
		if host == f.failAdd {
			return fmt.Errorf("add failed")
		}
		f.persistent = append(f.persistent, host)
	case rpcclient.ANRemove:
		if host == f.failRemove {
			return fmt.Errorf("remove failed")
		}
		kept := f.persistent[:0]
		for _, peer := range f.persistent {
			if peer != host {
				kept = append(kept, peer)
			}
		}
		f.persistent = kept
	}
	return nil
}

var _ = Describe("Bitcoin persistent peer reconciliation", func() {
	It("adds desired peers and removes only peers previously managed by Kiln", func() {
		rpc := &fakeBitcoinPeerRPC{
			persistent: []string{"manual.example:8333", "old.example:8333", "existing.example:8333"},
		}

		managed, err := reconcileBitcoinPeers(
			rpc,
			[]string{"new.example:8333", "existing.example:8333"},
			[]string{"old.example:8333"},
		)

		Expect(err).ToNot(HaveOccurred())
		Expect(managed).To(Equal([]string{"existing.example:8333", "new.example:8333"}))
		Expect(rpc.calls).To(ConsistOf(
			"remove:old.example:8333",
			"add:new.example:8333",
		))
		Expect(rpc.persistent).To(ConsistOf(
			"manual.example:8333",
			"existing.example:8333",
			"new.example:8333",
		))
	})

	It("adopts an already-persistent desired peer without reconnecting it", func() {
		rpc := &fakeBitcoinPeerRPC{persistent: []string{"peer.example:8333"}}

		managed, err := reconcileBitcoinPeers(rpc, []string{"peer.example:8333"}, nil)

		Expect(err).ToNot(HaveOccurred())
		Expect(managed).To(Equal([]string{"peer.example:8333"}))
		Expect(rpc.calls).To(BeEmpty())
	})

	It("removes managed peers when the desired set becomes empty without touching manual peers", func() {
		rpc := &fakeBitcoinPeerRPC{
			persistent: []string{"manual.example:8333", "managed.example:8333"},
		}

		managed, err := reconcileBitcoinPeers(rpc, nil, []string{"managed.example:8333"})

		Expect(err).ToNot(HaveOccurred())
		Expect(managed).To(BeEmpty())
		Expect(rpc.calls).To(Equal([]string{"remove:managed.example:8333"}))
		Expect(rpc.persistent).To(Equal([]string{"manual.example:8333"}))
	})

	It("does not report a failed addition as managed", func() {
		rpc := &fakeBitcoinPeerRPC{failAdd: "broken.example:8333"}

		managed, err := reconcileBitcoinPeers(rpc, []string{"broken.example:8333"}, nil)

		Expect(err).To(MatchError(ContainSubstring("add persistent peer")))
		Expect(managed).To(BeNil())
	})

	It("does not report a failed removal as reconciled", func() {
		rpc := &fakeBitcoinPeerRPC{
			persistent: []string{"broken.example:8333"},
			failRemove: "broken.example:8333",
		}

		managed, err := reconcileBitcoinPeers(rpc, nil, []string{"broken.example:8333"})

		Expect(err).To(MatchError(ContainSubstring("remove persistent peer")))
		Expect(managed).To(BeNil())
	})
})
