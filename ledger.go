package wavelet

import (
	"context"
	"encoding/binary"
	"github.com/perlin-network/wavelet/avl"
	"github.com/perlin-network/wavelet/common"
	"github.com/perlin-network/wavelet/log"
	"github.com/perlin-network/wavelet/store"
	"github.com/perlin-network/wavelet/sys"
	"github.com/pkg/errors"
	"golang.org/x/crypto/blake2b"
	"math"
	"sort"
	"time"
)

type Ledger struct {
	ctx    context.Context
	cancel context.CancelFunc

	accounts *accounts
	graph    *Graph

	snowball *Snowball

	processors map[byte]TransactionProcessor

	rounds map[uint64]Round
	round  uint64
}

func NewLedger() *Ledger {
	ctx, cancel := context.WithCancel(context.Background())

	accounts := newAccounts(store.NewInmem())
	graph := NewGraph()

	round := performInception(accounts.tree, nil)

	return &Ledger{
		ctx:    ctx,
		cancel: cancel,

		accounts: accounts,
		graph:    graph,

		snowball: NewSnowball().WithK(sys.SnowballQueryK).WithAlpha(sys.SnowballQueryAlpha).WithBeta(sys.SnowballQueryBeta),

		processors: map[byte]TransactionProcessor{
			sys.TagNop:      ProcessNopTransaction,
			sys.TagTransfer: ProcessTransferTransaction,
			sys.TagContract: ProcessContractTransaction,
			sys.TagStake:    ProcessStakeTransaction,
		},

		rounds: map[uint64]Round{round.idx: round},
		round:  1,
	}
}

func (l *Ledger) Run() {
	go l.accounts.gcLoop(l.ctx)
	go l.receivingLoop(l.ctx)
	go l.gossipingLoop(l.ctx)
	go l.queryingLoop(l.ctx)
}

func (l *Ledger) receivingLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (l *Ledger) gossipingLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		}
	}
}

func (l *Ledger) queryingLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := l.step(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}

const MinDifficulty = 8

// step atomically maintains the ledgers graph, and divides the graph from the bottom up into rounds.
//
// It maintains the ledgers Snowball instance while dividing up rounds, to see what the network believes
// is the preferred critical transaction selected to finalize the current ledgers round, and also be the
// root transaction for the next new round.
//
// It should be called repetitively as fast as possible in an infinite for loop, in a separate goroutine
// away from any other goroutines associated to the ledger.
func (l *Ledger) step(ctx context.Context) error {
	root := l.rounds[l.round-1].root

	if l.snowball.Preferred() == nil { // If we do not prefer any critical transaction yet, find a critical transaction to initially prefer first.
		difficulty := root.ExpectedDifficulty(MinDifficulty)

		var eligible []*Transaction // Find all critical transactions for the current round.

		for i := difficulty; i < math.MaxUint8; i++ {
			candidates, exists := l.graph.seedIndex[difficulty]

			if !exists {
				continue
			}

			for candidateID := range candidates {
				candidate := l.graph.transactions[candidateID]

				if candidate.Depth > root.Depth {
					eligible = append(eligible, candidate)
				}
			}
		}

		if len(eligible) == 0 { // If there are no critical transactions for the round yet, discontinue.
			return errors.New("no critical transactions available in round yet")
		}

		// Sort critical transactions by their depth, and pick the critical transaction
		// with the smallest depth as the nodes initial preferred transaction.
		//
		// The final selected critical transaction might change after a couple of
		// rounds with Snowball.

		sort.Slice(eligible, func(i, j int) bool {
			return eligible[i].Depth < eligible[j].Depth
		})

		proposed := eligible[0]

		state, err := l.collapseRound(l.round, proposed, true)

		if err != nil {
			return errors.Wrap(err, "got an error collapsing down tx to get merkle root")
		}

		initial := NewRound(l.round, state.Checksum(), *proposed)
		l.snowball.Prefer(&initial)
	}

	// TODO(kenta): query our peers, weigh their stakes, and find the response with the maximum
	// 	votes from our peers.

	var elected *Round

	l.snowball.Tick(elected)

	if l.snowball.Decided() {
		preferred := l.snowball.Preferred()
		root := l.graph.transactions[preferred.root.id]

		state, err := l.collapseRound(preferred.idx, root, true)

		if err != nil {
			return errors.Wrap(err, "got an error finalizing a round")
		}

		if state.Checksum() != preferred.merkle {
			return errors.Errorf("expected finalized rounds merkle root to be %x, but got %x", preferred.merkle, state.Checksum())
		}

		l.snowball.Reset()

		l.rounds[l.round] = *preferred
		l.round++

		// TODO(kenta): prune knowledge of rounds over time, say after 30 rounds and
		// 	also wipe away traces of their transactions.
	}

	return nil
}

// collapseRound takes all transactions recorded in the graph view so far, and applies
// all valid and available ones to a snapshot of all accounts stored in the ledger.
//
// It returns an updated accounts snapshot after applying all finalized transactions.
func (l *Ledger) collapseRound(round uint64, tx *Transaction, logging bool) (*avl.Tree, error) {
	snapshot := l.accounts.snapshot()
	snapshot.SetViewID(round + 1)

	root := l.rounds[l.round-1].root

	visited := map[common.TransactionID]struct{}{
		root.id: {},
	}

	aq := AcquireQueue()
	defer ReleaseQueue(aq)

	for _, parentID := range tx.ParentIDs {
		if parentID == root.id {
			continue
		}

		visited[parentID] = struct{}{}

		if parent, exists := l.graph.transactions[parentID]; exists {
			aq.PushBack(parent)
		} else {
			return snapshot, errors.Errorf("missing parent to correctly collapse down ledger state from critical transaction %x", tx.id)
		}
	}

	bq := AcquireQueue()
	defer ReleaseQueue(bq)

	for aq.Len() > 0 {
		popped := aq.PopFront().(*Transaction)

		for _, parentID := range popped.ParentIDs {
			if _, seen := visited[parentID]; !seen {
				visited[parentID] = struct{}{}

				if parent, exists := l.graph.transactions[parentID]; exists {
					aq.PushBack(parent)
				} else {
					return snapshot, errors.Errorf("missing ancestor to correctly collapse down ledger state from critical transaction %x", tx.id)
				}
			}
		}

		bq.PushBack(popped)
	}

	// Apply transactions in reverse order from the root of the view-graph all the way up to the newly
	// created critical transaction.
	for bq.Len() > 0 {
		popped := bq.PopBack().(*Transaction)

		// If any errors occur while applying our transaction to our accounts
		// snapshot, silently log it and continue applying other transactions.
		if err := l.rewardValidators(snapshot, popped, logging); err != nil {
			if logging {
				logger := log.Node()
				logger.Warn().Err(err).Msg("Failed to reward a validator while collapsing down transactions.")
			}
		}

		if err := l.applyTransactionToSnapshot(snapshot, popped); err != nil {
			if logging {
				logger := log.TX(popped.id, popped.Sender, popped.ParentIDs, popped.Tag, popped.Payload, "failed")
				logger.Log().Err(err).Msg("Failed to apply transaction to the ledger.")
			}
		} else {
			if logging {
				logger := log.TX(popped.id, popped.Sender, popped.ParentIDs, popped.Tag, popped.Payload, "applied")
				logger.Log().Msg("Successfully applied transaction to the ledger.")
			}
		}
	}

	//l.cacheAccounts.put(tx.getCriticalSeed(), snapshot)
	return snapshot, nil
}

func (l *Ledger) applyTransactionToSnapshot(ss *avl.Tree, tx *Transaction) error {
	ctx := newTransactionContext(ss, tx)

	if err := ctx.apply(l.processors); err != nil {
		return errors.Wrap(err, "could not apply transaction to snapshot")
	}

	return nil
}

func (l *Ledger) rewardValidators(ss *avl.Tree, tx *Transaction, logging bool) error {
	var candidates []*Transaction
	var stakes []uint64
	var totalStake uint64

	visited := make(map[common.AccountID]struct{})

	q := AcquireQueue()
	defer ReleaseQueue(q)

	for _, parentID := range tx.ParentIDs {
		if parent, exists := l.graph.transactions[parentID]; exists {
			q.PushBack(parent)
		}

		visited[parentID] = struct{}{}
	}

	// Ignore error; should be impossible as not using HMAC mode.
	hasher, _ := blake2b.New256(nil)

	var depthCounter uint64
	var lastDepth = tx.Depth

	for q.Len() > 0 {
		popped := q.PopFront().(*Transaction)

		if popped.Depth != lastDepth {
			lastDepth = popped.Depth
			depthCounter++
		}

		// If we exceed the max eligible depth we search for candidate
		// validators to reward from, stop traversing.
		if depthCounter >= sys.MaxEligibleParentsDepthDiff {
			break
		}

		// Filter for all ancestral transactions not from the same sender,
		// and within the desired graph depth.
		if popped.Sender != tx.Sender {
			stake, _ := ReadAccountStake(ss, popped.Sender)

			if stake > sys.MinimumStake {
				candidates = append(candidates, popped)
				stakes = append(stakes, stake)

				totalStake += stake

				// Record entropy source.
				if _, err := hasher.Write(popped.id[:]); err != nil {
					return errors.Wrap(err, "stake: failed to hash transaction id for entropy source")
				}
			}
		}

		for _, parentID := range popped.ParentIDs {
			if _, seen := visited[parentID]; !seen {
				if parent, exists := l.graph.transactions[parentID]; exists {
					q.PushBack(parent)
				}

				visited[parentID] = struct{}{}
			}
		}
	}

	// If there are no eligible rewardee candidates, do not reward anyone.
	if len(candidates) == 0 || len(stakes) == 0 || totalStake == 0 {
		return nil
	}

	entropy := hasher.Sum(nil)
	acc, threshold := float64(0), float64(binary.LittleEndian.Uint64(entropy)%uint64(0xffff))/float64(0xffff)

	var rewardee *Transaction

	// Model a weighted uniform distribution by a random variable X, and select
	// whichever validator has a weight X ≥ X' as a reward recipient.
	for i, tx := range candidates {
		acc += float64(stakes[i]) / float64(totalStake)

		if acc >= threshold {
			rewardee = tx
			break
		}
	}

	// If there is no selected transaction that deserves a reward, give the
	// reward to the last reward candidate.
	if rewardee == nil {
		rewardee = candidates[len(candidates)-1]
	}

	senderBalance, _ := ReadAccountBalance(ss, tx.Sender)
	recipientBalance, _ := ReadAccountBalance(ss, rewardee.Sender)

	fee := sys.TransactionFeeAmount

	if senderBalance < fee {
		return errors.Errorf("stake: sender %x does not have enough PERLs to pay transaction fees (requested %d PERLs) to %x", tx.Sender, fee, rewardee.Sender)
	}

	WriteAccountBalance(ss, tx.Sender, senderBalance-fee)
	WriteAccountBalance(ss, rewardee.Sender, recipientBalance+fee)

	if logging {
		logger := log.Stake("reward_validator")
		logger.Info().
			Hex("sender", tx.Sender[:]).
			Hex("recipient", rewardee.Sender[:]).
			Hex("sender_tx_id", tx.id[:]).
			Hex("rewardee_tx_id", rewardee.id[:]).
			Hex("entropy", entropy).
			Float64("acc", acc).
			Float64("threshold", threshold).Msg("Rewarded validator.")
	}

	return nil
}
