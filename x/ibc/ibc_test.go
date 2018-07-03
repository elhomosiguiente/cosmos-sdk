package ibc

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/crypto"
	dbm "github.com/tendermint/tendermint/libs/db"
	"github.com/tendermint/tendermint/libs/log"

	bam "github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/store"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/wire"
	"github.com/cosmos/cosmos-sdk/x/auth"
)

var testCodespace = sdk.CodespaceUndefined

// AccountMapper(/Keeper) and IBCMapper should use different StoreKey later

func defaultContext(keys ...sdk.StoreKey) sdk.Context {
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db)
	for _, key := range keys {
		cms.MountStoreWithDB(key, sdk.StoreTypeIAVL, db)
	}
	cms.LoadLatestVersion()
	ctx := sdk.NewContext(cms, abci.Header{}, false, log.NewNopLogger())
	return ctx
}

func newAddress() crypto.Address {
	return crypto.GenPrivKeyEd25519().PubKey().Address()
}

type remoteSavePayload struct {
	key   []byte
	value []byte
}

func (p remoteSavePayload) Type() string {
	return "remote"
}

func (p remoteSavePayload) ValidateBasic() sdk.Error {
	return nil
}

type remoteSaveFailPayload struct {
	remoteSavePayload
}

func (p remoteSaveFailPayload) Type() string {
	return "remote"
}

func (p remoteSaveFailPayload) ValidateBasic() sdk.Error {
	return nil
}

type remoteSaveMsg struct {
	payload   remoteSavePayload
	destChain string
}

func (msg remoteSaveMsg) Get(key interface{}) interface{} {
	return nil
}

func (msg remoteSaveMsg) GetSignBytes() []byte {
	return nil
}

func (msg remoteSaveMsg) GetSigners() []sdk.Address {
	return []sdk.Address{}
}

func (msg remoteSaveMsg) Type() string {
	return "remote"
}

func (msg remoteSaveMsg) ValidateBasic() sdk.Error {
	return nil
}

func makeCodec() *wire.Codec {
	var cdc = wire.NewCodec()

	// Register Msgs
	cdc.RegisterInterface((*sdk.Msg)(nil), nil)
	cdc.RegisterConcrete(remoteSaveMsg{}, "test/remote/remoteSave", nil)
	cdc.RegisterConcrete(MsgReceive{}, "test/ibc/Receive", nil)

	// Register Payloads
	cdc.RegisterInterface((*Payload)(nil), nil)
	cdc.RegisterConcrete(remoteSavePayload{}, "test/payload/remoteSave", nil)
	cdc.RegisterConcrete(remoteSaveFailPayload{}, "test/payload/remoteSaveFail", nil)

	return cdc

}

func newIBCTestApp(logger log.Logger, db dbm.DB) *bam.BaseApp {
	cdc := makeCodec()
	app := bam.NewBaseApp("test", cdc, logger, db)

	key := sdk.NewKVStoreKey("remote")
	ibcKey := sdk.NewKVStoreKey("ibc")
	keeper := NewKeeper(cdc, ibcKey, app.RegisterCodespace(DefaultCodespace))

	app.Router().
		AddRoute("remote", remoteSaveHandler(key, keeper)).
		AddRoute("ibc", NewHandler(keeper))

	app.MountStoresIAVL(key, ibcKey)
	err := app.LoadLatestVersion(key)
	if err != nil {
		panic(err)
	}

	app.InitChain(abci.RequestInitChain{})
	return app
}

func remoteSaveHandler(key sdk.StoreKey, ibck Keeper) sdk.Handler {
	return func(ctx sdk.Context, msg sdk.Msg) sdk.Result {
		ibcc := ibck.Channel(sdk.NewPrefixStoreGetter(key, []byte("ibctest")))
		switch msg := msg.(type) {
		case remoteSaveMsg:
			return handleRemoteSaveMsg(ctx, ibcc, msg)
		case MsgReceive:
			return ibcc.Receive(func(ctx sdk.Context, p Payload) (Payload, sdk.Error) {
				switch p := p.(type) {
				case remoteSavePayload:
					return handleRemoteSavePayload(ctx, key, p)
				default:
					return nil, sdk.ErrUnknownRequest("")
				}
			}, ctx, msg)
		case MsgReceipt:
			return ibcc.Receipt(func(ctx sdk.Context, p Payload) {
				switch p := p.(type) {
				case remoteSaveFailPayload:
					handleRemoteSaveFailPayload(ctx, key, p)
				default:
					sdk.ErrUnknownRequest("")
				}
			}, ctx, msg)

		default:
			return sdk.ErrUnknownRequest("").Result()
		}
	}
}

func handleRemoteSaveMsg(ctx sdk.Context, ibcc Channel, msg remoteSaveMsg) sdk.Result {
	ibcc.Send(ctx, msg.payload, msg.destChain, testCodespace)
	return sdk.Result{}
}

func handleRemoteSavePayload(ctx sdk.Context, key sdk.StoreKey, p remoteSavePayload) (Payload, sdk.Error) {
	store := ctx.KVStore(key)
	if store.Has(p.key) {
		return remoteSaveFailPayload{p}, sdk.NewError(testCodespace, 1000, "Key already exists")
	}
	store.Set(p.key, p.value)
	return nil, nil
}

func handleRemoteSaveFailPayload(ctx sdk.Context, key sdk.StoreKey, p remoteSaveFailPayload) {
	return
}

func TestIBC(t *testing.T) {
	logger := log.NewTMLogger(log.NewSyncWriter(os.Stdout)).With("module", "sdk/app")
	db := dbm.NewMemDB()
	app := newIBCTestApp(logger, db)

	ctx := app.NewContext(false, abci.Header{})
	chainid := ctx.ChainID()

	// Open channel

	// Send IBC message
	payload := remoteSavePayload{
		key:   []byte("hello"),
		value: []byte("world"),
	}

	saveMsg := remoteSaveMsg{
		payload:   payload,
		destChain: chainid,
	}

	tx := auth.NewStdTx([]sdk.Msg{saveMsg}, auth.NewStdFee(0), []auth.StdSignature{}, "")

	var res sdk.Result

	res = app.Deliver(tx)
	assert.True(t, res.IsOK(), fmt.Sprintf("%+v", res))

	// Receive IBC message
	packet := Packet{
		Payload:   payload,
		SrcChain:  chainid,
		DestChain: chainid,
	}

	receiveMsg := MsgReceive{
		Packet: packet,
		/*	PacketProof: PacketProof{
			Sequence: 0,
		},*/
		Relayer: newAddress(),
	}

	tx.Msgs[0] = receiveMsg

	res = app.Deliver(tx)
	assert.True(t, res.IsOK())
	/*
		store := ctx.KVStore(key)
		val := store.Get(payload.key)
		assert.Equal(t, payload.value, val)
	*/

	tx.Msgs[0] = receiveMsg
	res = app.Deliver(tx)
	fmt.Printf("%+v\n", res)
	assert.False(t, res.IsOK())

	// Send another IBC message and receive it
	// It has duplicated key bytes so fails
	tx.Msgs[0] = saveMsg
	res = app.Deliver(tx)
	assert.True(t, res.IsOK())

	receiveMsg = MsgReceive{
		Packet: packet,
		/*PacketProof: PacketProof{
			Sequence: 1,
		},*/
		Relayer: newAddress(),
	}

	tx.Msgs[0] = receiveMsg
	res = app.Deliver(tx)
	assert.True(t, res.IsOK())

	// Return fail receipt
	packet.Payload = remoteSaveFailPayload{payload}

	receiptMsg := MsgReceipt{
		Packet: packet,
		/*PacketProof: PacketProof{
			Sequence: 0,
		},*/
		Relayer: newAddress(),
	}

	tx.Msgs[0] = receiptMsg
	res = app.Deliver(tx)
	assert.True(t, res.IsOK())

	// Cleanup receive queue
	receiveCleanupMsg := MsgReceiveCleanup{
		Sequence: 2,
		SrcChain: chainid,
		//CleanupProof: CleanupProof{},
		Cleaner: newAddress(),
	}

	tx.Msgs[0] = receiveCleanupMsg
	res = app.Deliver(tx)
	assert.True(t, res.IsOK())

	// Cleanup receipt queue
	receiptCleanupMsg := MsgReceiptCleanup{
		Sequence: 1,
		SrcChain: chainid,
		//CleanupProof: CleanupProof{},
		Cleaner: newAddress(),
	}

	tx.Msgs[0] = receiptCleanupMsg
	res = app.Deliver(tx)
	assert.True(t, res.IsOK())

	unknownMsg := sdk.NewTestMsg(newAddress())
	tx.Msgs[0] = unknownMsg
	res = app.Deliver(tx)
	assert.False(t, res.IsOK())
}
