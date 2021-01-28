package com.hedera.mirror.importer.parser.record.transactionhandler;

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

import com.hedera.mirror.importer.domain.Entities;
import com.hedera.mirror.importer.domain.EntityId;
import com.hedera.mirror.importer.domain.Transaction;
import com.hedera.mirror.importer.parser.domain.RecordItem;

/**
 * TransactionHandler interface abstracts the logic for processing different kinds for transactions. For each
 * transaction type, there exists an unique implementation of TransactionHandler which encapsulates all logic specific
 * to processing of that transaction type. A single {@link com.hederahashgraph.api.proto.java.Transaction} and its
 * associated info (TransactionRecord, deserialized TransactionBody, etc) are all encapsulated together in a single
 * {@link RecordItem}. Hence, most functions of this interface require RecordItem as a parameter.
 */
public interface TransactionHandler {
    /**
     * @return main entity associated with this transaction
     */
    EntityId getEntity(RecordItem recordItem);

    default EntityId getProxyAccount(RecordItem recordItem) {
        return null;
    }

    default EntityId getAutoRenewAccount(RecordItem recordItem) {
        return null;
    }

    /**
     * Override to return true if an implementation wants to update the entity returned by
     * {@link #getEntity(RecordItem)}.
     */
    default boolean updatesEntity() {
        return false;
    }

    /**
     * Override to update fields of the entity.
     * If {@link #updatesEntity()} returns true, and {@link #getEntity(RecordItem)} returns a non-null id, and the
     * transaction is successful, then this function will be called.
     * @param entity latest state of entity (fetched from the repo)
     */
    default void updateEntity(Entities entity, RecordItem recordItem) {
    }

    /**
     * Override to update fields of the Transaction's (domain) fields.
     */
    default void updateTransaction(Transaction transaction, RecordItem recordItem) {
    }
}
