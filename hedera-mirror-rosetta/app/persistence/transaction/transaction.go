/*-
 * ‌
 * Hedera Mirror Node
 * ​
 * Copyright (C) 2019 - 2021 Hedera Hashgraph, LLC
 * ​
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 * ‍
 */

package transaction

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sync"

	rTypes "github.com/coinbase/rosetta-sdk-go/types"
	"github.com/hashgraph/hedera-mirror-node/hedera-mirror-rosetta/app/domain/repositories"
	entityid "github.com/hashgraph/hedera-mirror-node/hedera-mirror-rosetta/app/domain/services/encoding"
	"github.com/hashgraph/hedera-mirror-node/hedera-mirror-rosetta/app/domain/types"
	hErrors "github.com/hashgraph/hedera-mirror-node/hedera-mirror-rosetta/app/errors"
	dbTypes "github.com/hashgraph/hedera-mirror-node/hedera-mirror-rosetta/app/persistence/types"
	hexUtils "github.com/hashgraph/hedera-mirror-node/hedera-mirror-rosetta/tools/hex"
	"github.com/hashgraph/hedera-mirror-node/hedera-mirror-rosetta/tools/maphelper"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	batchSize                   = 2000
	tableNameTransactionResults = "t_transaction_results"
	tableNameTransactionTypes   = "t_transaction_types"
	transactionResultSuccess    = 22
)

const (
	andTransactionHashFilter = " and transaction_hash = @hash"
	orderByConsensusNs       = " order by consensus_ns"
	selectTransactionResults = "select * from " + tableNameTransactionResults
	selectTransactionTypes   = "select * from " + tableNameTransactionTypes
	// selectTransactionsInTimestampRange selects the transactions with its crypto transfers in json, non-fee transfers
	// in json, token transfers in json, and optionally the token information when the transaction is token create,
	// token delete, or token update. Note the three token transactions are the ones the entity_id in the transaction
	// table is its related token id and require an extra rosetta operation
	selectTransactionsInTimestampRange = `select
                                            t.consensus_ns,
                                            t.payer_account_id,
                                            t.transaction_hash as hash,
                                            t.result,
                                            t.type,
                                            coalesce((
                                              select json_agg(json_build_object(
                                                'account_id', entity_id,
                                                'amount', amount))
                                              from crypto_transfer where consensus_timestamp = t.consensus_ns
                                            ), '[]') as crypto_transfers,
                                            case
                                              when t.type = 14 then coalesce((
                                                  select json_agg(json_build_object(
                                                      'account_id', entity_id, 
                                                      'amount', amount
                                                    ))
                                                  from non_fee_transfer
                                                  where consensus_timestamp = t.consensus_ns
                                                ), '[]')
                                              else '[]'
                                            end as non_fee_transfers,
                                            coalesce((
                                              select json_agg(json_build_object(
                                                  'account_id', account_id,
                                                  'amount', amount,
                                                  'decimals', tk.decimals,
                                                  'token_id', tkt.token_id
                                                ))
                                              from token_transfer tkt
                                              join token tk on tk.token_id = tkt.token_id
                                              where tkt.consensus_timestamp = t.consensus_ns
                                            ), '[]') as token_transfers,
                                            case
                                              when t.type in (29, 35, 36) then coalesce((
                                                  select json_build_object(
                                                    'token_id', token_id,
                                                    'decimals', decimals,
                                                    'freeze_default', freeze_default,
                                                    'initial_supply', initial_supply
                                                  )
                                                  from token
                                                  where token_id = t.entity_id
                                                ), '{}')
                                              else '{}'
                                            end as token
                                          from transaction t
                                          where consensus_ns >= @start and consensus_ns <= @end`
	selectTransactionsByHashInTimestampRange  = selectTransactionsInTimestampRange + andTransactionHashFilter
	selectTransactionsInTimestampRangeOrdered = selectTransactionsInTimestampRange + orderByConsensusNs
)

type transactionType struct {
	ProtoID int    `gorm:"type:integer;primaryKey"`
	Name    string `gorm:"size:30"`
}

type transactionResult struct {
	ProtoID int    `gorm:"type:integer;primaryKey"`
	Result  string `gorm:"size:100"`
}

// TableName - Set table name of the Transaction Types to be `t_transaction_types`
func (transactionType) TableName() string {
	return tableNameTransactionTypes
}

// TableName - Set table name of the Transaction Results to be `t_transaction_results`
func (transactionResult) TableName() string {
	return tableNameTransactionResults
}

// transaction maps to the transaction query which returns the required transaction fields, CryptoTransfers json string,
// NonFeeTransfers json string, TokenTransfers json string, and Token definition json string
type transaction struct {
	ConsensusNs     int64
	Hash            []byte
	PayerAccountId  int64
	Result          int16
	Type            int16
	CryptoTransfers string
	NonFeeTransfers string
	TokenTransfers  string
	Token           string
}

func (t transaction) getHashString() string {
	return hexUtils.SafeAddHexPrefix(hex.EncodeToString(t.Hash))
}

type transfer interface {
	getAccount() types.Account
	getAmount() types.Amount
}

type hbarTransfer struct {
	AccountId entityid.EntityId `json:"account_id"`
	Amount    int64             `json:"amount"`
}

func (t hbarTransfer) getAccount() types.Account {
	return types.Account{EntityId: t.AccountId}
}

func (t hbarTransfer) getAmount() types.Amount {
	return &types.HbarAmount{Value: t.Amount}
}

type tokenTransfer struct {
	AccountId entityid.EntityId `json:"account_id"`
	Amount    int64             `json:"amount"`
	Decimals  int64             `json:"decimals"`
	TokenId   entityid.EntityId `json:"token_id"`
}

func (t tokenTransfer) getAccount() types.Account {
	return types.Account{EntityId: t.AccountId}
}

func (t tokenTransfer) getAmount() types.Amount {
	return &types.TokenAmount{
		Decimals: t.Decimals,
		TokenId:  t.TokenId,
		Value:    t.Amount,
	}
}

type token struct {
	Decimals      int64             `json:"decimals"`
	FreezeDefault bool              `json:"freeze_default"`
	InitialSupply int64             `json:"initial_supply"`
	TokenId       entityid.EntityId `json:"token_id"`
}

func (t token) getAmount() types.Amount {
	return &types.TokenAmount{
		TokenId:  t.TokenId,
		Decimals: t.Decimals,
		Value:    0,
	}
}

// transactionRepository struct that has connection to the Database
type transactionRepository struct {
	once     sync.Once
	dbClient *gorm.DB
	results  map[int]string
	types    map[int]string
}

// NewTransactionRepository creates an instance of a TransactionRepository struct
func NewTransactionRepository(dbClient *gorm.DB) repositories.TransactionRepository {
	return &transactionRepository{dbClient: dbClient}
}

// Types returns map of all transaction types
func (tr *transactionRepository) Types() (map[int]string, *rTypes.Error) {
	if tr.types == nil {
		err := tr.retrieveTransactionTypesAndResults()
		if err != nil {
			return nil, err
		}
	}
	return tr.types, nil
}

// Results returns map of all transaction results
func (tr *transactionRepository) Results() (map[int]string, *rTypes.Error) {
	if tr.results == nil {
		err := tr.retrieveTransactionTypesAndResults()
		if err != nil {
			return nil, err
		}
	}
	return tr.results, nil
}

// TypesAsArray returns all Transaction type names as an array
func (tr *transactionRepository) TypesAsArray() ([]string, *rTypes.Error) {
	transactionTypes, err := tr.Types()
	if err != nil {
		return nil, err
	}
	return maphelper.GetStringValuesFromIntStringMap(transactionTypes), nil
}

// FindBetween retrieves all Transactions between the provided start and end timestamp
func (tr *transactionRepository) FindBetween(start, end int64) ([]*types.Transaction, *rTypes.Error) {
	if start > end {
		return nil, hErrors.ErrStartMustNotBeAfterEnd
	}

	transactions := make([]*transaction, 0)

	for start <= end {
		transactionsBatch := make([]*transaction, 0)
		tr.dbClient.
			Raw(selectTransactionsInTimestampRangeOrdered, sql.Named("start", start), sql.Named("end", end)).
			Limit(batchSize).
			Find(&transactionsBatch)
		transactions = append(transactions, transactionsBatch...)

		if len(transactionsBatch) < batchSize {
			break
		}

		start = transactionsBatch[len(transactionsBatch)-1].ConsensusNs + 1
	}

	hashes := make([]string, 0)
	sameHashMap := make(map[string][]*transaction)
	for _, t := range transactions {
		h := t.getHashString()
		if _, ok := sameHashMap[h]; !ok {
			// save the unique hashes in chronological order
			hashes = append(hashes, h)
		}

		sameHashMap[h] = append(sameHashMap[h], t)
	}

	res := make([]*types.Transaction, 0, len(sameHashMap))
	for _, hash := range hashes {
		sameHashTransactions := sameHashMap[hash]
		transaction, err := tr.constructTransaction(sameHashTransactions)
		if err != nil {
			return nil, err
		}
		res = append(res, transaction)
	}
	return res, nil
}

// FindByHashInBlock retrieves a transaction by Hash
func (tr *transactionRepository) FindByHashInBlock(
	hashStr string,
	consensusStart int64,
	consensusEnd int64,
) (*types.Transaction, *rTypes.Error) {
	var transactions []*transaction
	transactionHash, err := hex.DecodeString(hexUtils.SafeRemoveHexPrefix(hashStr))
	if err != nil {
		return nil, hErrors.ErrInvalidTransactionIdentifier
	}

	tr.dbClient.
		Raw(
			selectTransactionsByHashInTimestampRange,
			sql.Named("hash", transactionHash),
			sql.Named("start", consensusStart),
			sql.Named("end", consensusEnd),
		).
		Find(&transactions)
	if len(transactions) == 0 {
		return nil, hErrors.ErrTransactionNotFound
	}

	transaction, rErr := tr.constructTransaction(transactions)
	if rErr != nil {
		return nil, rErr
	}

	return transaction, nil
}

func (tr *transactionRepository) retrieveTransactionTypes() []transactionType {
	var transactionTypes []transactionType
	tr.dbClient.Raw(selectTransactionTypes).Find(&transactionTypes)
	return transactionTypes
}

func (tr *transactionRepository) retrieveTransactionResults() []transactionResult {
	var tResults []transactionResult
	tr.dbClient.Raw(selectTransactionResults).Find(&tResults)
	return tResults
}

func (tr *transactionRepository) constructTransaction(sameHashTransactions []*transaction) (
	*types.Transaction,
	*rTypes.Error,
) {
	transactionTypes, err := tr.Types()
	if err != nil {
		return nil, err
	}

	transactionResults, err := tr.Results()
	if err != nil {
		return nil, err
	}

	tResult := &types.Transaction{Hash: sameHashTransactions[0].getHashString()}
	operations := make([]*types.Operation, 0)
	success := transactionResults[transactionResultSuccess]

	for _, transaction := range sameHashTransactions {
		cryptoTransfers := make([]hbarTransfer, 0)
		if err := json.Unmarshal([]byte(transaction.CryptoTransfers), &cryptoTransfers); err != nil {
			return nil, hErrors.ErrInternalServerError
		}

		nonFeeTransfers := make([]hbarTransfer, 0)
		if err := json.Unmarshal([]byte(transaction.NonFeeTransfers), &nonFeeTransfers); err != nil {
			return nil, hErrors.ErrInternalServerError
		}

		tokenTransfers := make([]tokenTransfer, 0)
		if err := json.Unmarshal([]byte(transaction.TokenTransfers), &tokenTransfers); err != nil {
			return nil, hErrors.ErrInternalServerError
		}

		token := &token{}
		if err := json.Unmarshal([]byte(transaction.Token), token); err != nil {
			return nil, hErrors.ErrInternalServerError
		}

		transactionResult := transactionResults[int(transaction.Result)]
		transactionType := transactionTypes[int(transaction.Type)]

		nonFeeTransferMap := aggregateNonFeeTransfers(nonFeeTransfers)
		adjustedCryptoTransfers := adjustCryptoTransfers(cryptoTransfers, nonFeeTransferMap)

		operations = tr.appendHbarTransferOperations(transactionResult, transactionType, nonFeeTransfers, operations)
		// crypto transfers are always successful regardless of the transaction result
		operations = tr.appendHbarTransferOperations(success, transactionType, adjustedCryptoTransfers, operations)
		operations = tr.appendTokenTransferOperations(transactionResult, transactionType, tokenTransfers, operations)

		if !token.TokenId.IsZero() {
			operation, err := getTokenOperation(len(operations), token, transaction, transactionResult, transactionType)
			if err != nil {
				return nil, err
			}
			operations = append(operations, operation)
		}
	}

	tResult.Operations = operations
	return tResult, nil
}

func (tr *transactionRepository) appendHbarTransferOperations(
	transactionResult string,
	transactionType string,
	hbarTransfers []hbarTransfer,
	operations []*types.Operation,
) []*types.Operation {
	transfers := make([]transfer, 0, len(hbarTransfers))
	for _, hbarTransfer := range hbarTransfers {
		transfers = append(transfers, hbarTransfer)
	}

	return tr.appendTransferOperations(transactionResult, transactionType, transfers, operations)
}

func (tr *transactionRepository) appendTokenTransferOperations(
	transactionResult string,
	transactionType string,
	tokenTransfers []tokenTransfer,
	operations []*types.Operation,
) []*types.Operation {
	transfers := make([]transfer, 0, len(tokenTransfers))
	for _, tokenTransfer := range tokenTransfers {
		transfers = append(transfers, tokenTransfer)
	}

	return tr.appendTransferOperations(transactionResult, transactionType, transfers, operations)
}

func (tr *transactionRepository) appendTransferOperations(
	transactionResult string,
	transactionType string,
	transfers []transfer,
	operations []*types.Operation,
) []*types.Operation {
	for _, transfer := range transfers {
		operations = append(operations, &types.Operation{
			Index:   int64(len(operations)),
			Type:    transactionType,
			Status:  transactionResult,
			Account: transfer.getAccount(),
			Amount:  transfer.getAmount(),
		})
	}
	return operations
}

func (tr *transactionRepository) retrieveTransactionTypesAndResults() *rTypes.Error {
	typeArray := tr.retrieveTransactionTypes()
	resultArray := tr.retrieveTransactionResults()

	if len(typeArray) == 0 {
		log.Warn("No Transaction Types were found in the database.")
		return hErrors.ErrOperationTypesNotFound
	}

	if len(resultArray) == 0 {
		log.Warn("No Transaction Results were found in the database.")
		return hErrors.ErrOperationResultsNotFound
	}

	tr.once.Do(func() {
		tr.types = make(map[int]string)
		for _, t := range typeArray {
			tr.types[t.ProtoID] = t.Name
		}

		tr.results = make(map[int]string)
		for _, s := range resultArray {
			tr.results[s.ProtoID] = s.Result
		}
	})

	return nil
}

func IsTransactionResultSuccessful(result int) bool {
	return result == transactionResultSuccess
}

func constructAccount(encodedId int64) (types.Account, *rTypes.Error) {
	account, err := types.NewAccountFromEncodedID(encodedId)
	if err != nil {
		log.Errorf(hErrors.CreateAccountDbIdFailed, encodedId)
		return types.Account{}, hErrors.ErrInternalServerError
	}
	return account, nil
}

func adjustCryptoTransfers(
	cryptoTransfers []hbarTransfer,
	nonFeeTransferMap map[int64]int64,
) []hbarTransfer {
	cryptoTransferMap := make(map[int64]hbarTransfer)
	for _, transfer := range cryptoTransfers {
		key := transfer.AccountId.EncodedId
		cryptoTransferMap[key] = hbarTransfer{
			AccountId: transfer.AccountId,
			Amount:    transfer.Amount + cryptoTransferMap[key].Amount,
		}
	}

	adjusted := make([]hbarTransfer, 0, len(cryptoTransfers))
	for key, aggregated := range cryptoTransferMap {
		amount := aggregated.Amount - nonFeeTransferMap[key]
		if amount != 0 {
			adjusted = append(adjusted, hbarTransfer{
				AccountId: aggregated.AccountId,
				Amount:    amount,
			})
		}
	}

	return adjusted
}

func aggregateNonFeeTransfers(nonFeeTransfers []hbarTransfer) map[int64]int64 {
	nonFeeTransferMap := make(map[int64]int64)

	// the original transfer list from the transaction body
	for _, transfer := range nonFeeTransfers {
		// the original transfer list may have multiple entries for one entity, so accumulate it
		nonFeeTransferMap[transfer.AccountId.EncodedId] += transfer.Amount
	}

	return nonFeeTransferMap
}

func getTokenOperation(
	index int,
	token *token,
	transaction *transaction,
	transactionResult string,
	transactionType string,
) (*types.Operation, *rTypes.Error) {
	payerId, rErr := constructAccount(transaction.PayerAccountId)
	if rErr != nil {
		return nil, rErr
	}

	operation := &types.Operation{
		Index:   int64(index),
		Type:    transactionType,
		Status:  transactionResult,
		Account: payerId,
		Amount:  token.getAmount(),
	}

	if transaction.Type == dbTypes.TransactionTypeTokenCreation {
		// token creation shouldn't have Amount
		operation.Amount = nil
		metadata := make(map[string]interface{})
		operation.Metadata = metadata

		// best effort for immutable fields
		metadata["decimals"] = token.Decimals
		metadata["freeze_default"] = token.FreezeDefault
		metadata["initial_supply"] = token.InitialSupply
	}

	return operation, nil
}
