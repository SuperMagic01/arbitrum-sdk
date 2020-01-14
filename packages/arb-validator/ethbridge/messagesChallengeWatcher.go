/*
 * Copyright 2020, Offchain Labs, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package ethbridge

import (
	"context"
	"errors"
	"strings"

	errors2 "github.com/pkg/errors"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/offchainlabs/arbitrum/packages/arb-util/common"
	"github.com/offchainlabs/arbitrum/packages/arb-validator/arbbridge"
	"github.com/offchainlabs/arbitrum/packages/arb-validator/ethbridge/messageschallenge"
)

var messagesBisectedID ethcommon.Hash
var messagesOneStepProofCompletedID ethcommon.Hash

func init() {
	parsed, err := abi.JSON(strings.NewReader(messageschallenge.MessagesChallengeABI))
	if err != nil {
		panic(err)
	}
	messagesBisectedID = parsed.Events["Bisected"].ID()
	messagesOneStepProofCompletedID = parsed.Events["OneStepProofCompleted"].ID()
}

type messagesChallengeWatcher struct {
	*bisectionChallengeWatcher
	contract *messageschallenge.MessagesChallenge
	client   *ethclient.Client
	address  ethcommon.Address
}

func newMessagesChallengeWatcher(address ethcommon.Address, client *ethclient.Client) (*messagesChallengeWatcher, error) {
	bisectionChallenge, err := newBisectionChallengeWatcher(address, client)
	if err != nil {
		return nil, err
	}
	messagesContract, err := messageschallenge.NewMessagesChallenge(address, client)
	if err != nil {
		return nil, errors2.Wrap(err, "Failed to connect to messagesChallenge")
	}

	return &messagesChallengeWatcher{
		bisectionChallengeWatcher: bisectionChallenge,
		contract:                  messagesContract,
		client:                    client,
		address:                   address,
	}, nil
}

func (c *messagesChallengeWatcher) topics() []ethcommon.Hash {
	tops := []ethcommon.Hash{
		messagesBisectedID,
		messagesOneStepProofCompletedID,
	}
	return append(tops, c.bisectionChallengeWatcher.topics()...)
}

func (c *messagesChallengeWatcher) StartConnection(ctx context.Context, startHeight *common.TimeBlocks, startLogIndex uint, eventChan chan<- arbbridge.Event, errChan chan<- error) error {
	filter := ethereum.FilterQuery{
		Addresses: []ethcommon.Address{c.address},
		Topics:    [][]ethcommon.Hash{c.topics()},
	}

	logCtx, cancelFunc := context.WithCancel(ctx)
	logChan, logErrChan, err := getLogs(logCtx, c.client, filter, startHeight, startLogIndex)
	if err != nil {
		return err
	}

	go func() {
		defer cancelFunc()
		for {
			select {
			case <-ctx.Done():
				break
			case evmLog, ok := <-logChan:
				if !ok {
					errChan <- errors.New("logChan terminated early")
					return
				}
				header, err := c.client.HeaderByHash(ctx, evmLog.BlockHash)
				if err != nil {
					errChan <- err
					return
				}
				chainInfo := getChainInfo(evmLog, header)
				event, err := c.parseMessagesEvent(chainInfo, evmLog)
				if err != nil {
					errChan <- err
					return
				}
				eventChan <- event
			case err := <-logErrChan:
				errChan <- err
				return
			}
		}
	}()
	return nil
}

func (c *messagesChallengeWatcher) parseMessagesEvent(chainInfo arbbridge.ChainInfo, log types.Log) (arbbridge.Event, error) {
	if log.Topics[0] == messagesBisectedID {
		eventVal, err := c.contract.ParseBisected(log)
		if err != nil {
			return nil, err
		}
		return arbbridge.MessagesBisectionEvent{
			ChainInfo:     chainInfo,
			ChainHashes:   hashSliceToHashes(eventVal.ChainHashes),
			SegmentHashes: hashSliceToHashes(eventVal.SegmentHashes),
			TotalLength:   eventVal.TotalLength,
			Deadline:      common.TimeTicks{Val: eventVal.DeadlineTicks},
		}, nil
	} else if log.Topics[0] == messagesOneStepProofCompletedID {
		_, err := c.contract.ParseOneStepProofCompleted(log)
		if err != nil {
			return nil, err
		}
		return arbbridge.OneStepProofEvent{
			ChainInfo: chainInfo,
		}, nil
	}
	return c.bisectionChallengeWatcher.parseBisectionEvent(chainInfo, log)
}
