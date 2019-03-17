package wavelet

//import (
//	"crypto/rand"
//	"github.com/perlin-network/noise/payload"
//	"github.com/perlin-network/noise/skademlia"
//	"github.com/perlin-network/wavelet/store"
//	"github.com/perlin-network/wavelet/sys"
//	"github.com/pkg/errors"
//	"github.com/stretchr/testify/assert"
//	"testing"
//	"time"
//)
//
//func createLedger() *Ledger {
//	genesisPath := "cmd/wavelet/config/genesis.json"
//
//	kv := store.NewInmem()
//
//	ledger := NewLedger(kv, genesisPath)
//	ledger.RegisterProcessor(sys.TagNop, ProcessNopTransaction)
//	ledger.RegisterProcessor(sys.TagTransfer, ProcessTransferTransaction)
//	ledger.RegisterProcessor(sys.TagContract, ProcessContractTransaction)
//	ledger.RegisterProcessor(sys.TagStake, ProcessStakeTransaction)
//
//	return ledger
//}
//
//func createNormalTransaction(t testing.TB, ledger *Ledger, keys *skademlia.Keypair) *Transaction {
//	var buf [200]byte
//	_, err := rand.Read(buf[:])
//	assert.NoError(t, err)
//
//	tx, err := ledger.NewTransaction(keys, sys.TagTransfer, buf[:])
//	assert.NoError(t, err)
//
//	return tx
//}
//
//func createCriticalTransaction(t testing.TB, ledger *Ledger, keys *skademlia.Keypair) *Transaction {
//	for {
//		// Add a transaction that might be a parent that lets our next transaction
//		// be a critical transaction.
//		candidateParent := createNormalTransaction(t, ledger, keys)
//
//		if err := ledger.ReceiveTransaction(candidateParent); errors.Cause(err) != VoteAccepted {
//			t.Fatalf("failed to receive ledger transaction %+v", err)
//		}
//
//		// Sleep for a single millisecond to let timestamps tick.
//		time.Sleep(1 * time.Millisecond)
//
//		tx := createNormalTransaction(t, ledger, keys)
//
//		if tx.IsCritical(ledger.Difficulty()) {
//			return tx
//		}
//	}
//}
//
//func TestSerializeNormalTransaction(t *testing.T) {
//	ledger := createLedger()
//	keys := skademlia.RandomKeys()
//
//	tx := createNormalTransaction(t, ledger, keys)
//
//	msg, err := tx.Read(payload.NewReader(tx.Write()))
//	assert.NoError(t, err)
//
//	assert.ObjectsAreEqual(tx, msg)
//}
//
//func TestSerializeCriticalTransaction(t *testing.T) {
//	ledger := createLedger()
//	keys := skademlia.RandomKeys()
//
//	tx := createCriticalTransaction(t, ledger, keys)
//
//	msg, err := tx.Read(payload.NewReader(tx.Write()))
//	assert.NoError(t, err)
//
//	assert.ObjectsAreEqual(tx, msg)
//}
//
//func BenchmarkNewTransaction(b *testing.B) {
//	b.StopTimer()
//
//	ledger := createLedger()
//	keys := skademlia.RandomKeys()
//
//	b.StartTimer()
//
//	for i := 0; i < b.N; i++ {
//		createNormalTransaction(b, ledger, keys)
//	}
//}
