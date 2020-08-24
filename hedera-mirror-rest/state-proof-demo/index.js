#!/usr/bin/env node
/*-
 * ‌
 * Hedera Mirror Node
 * ​
 * Copyright (C) 2019 - 2020 Hedera Hashgraph, LLC
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

'uses strict';

// external libraries
const {startUpScreen} = require('./startUp');
const {StateProofHandler} = require('./stateProofHandler');
const {getAPIResponse, readJSONFile} = require('./utils');

// get user input
const {transactionId, url, sample} = startUpScreen();

const getStateProofJson = async (url, test) => {
  console.log(`Use sample data : ${test}`);
  return test ? readJSONFile('stateProofSample.json') : getAPIResponse(url);
};

getStateProofJson(url, sample).then((stateProofJson) => {
  const missingFilesPrefix = 'Mirror node StateProof API returned insufficient number of files.';
  if (stateProofJson.address_books.length < 1) {
    console.error(`${missingFilesPrefix} At least 1 addressBook is expected`);
    return false;
  }

  if (stateProofJson.record_file.length < 1) {
    console.error(`${missingFilesPrefix} At least 1 record file is expected`);
    return false;
  }

  if (stateProofJson.signature_files.length < 2) {
    console.error(`${missingFilesPrefix} At least 2 signature files are expected`);
    return false;
  }

  // instantiate stateProofHandler which will parse files and extract needed data
  const stateProofHandler = new StateProofHandler(transactionId, stateProofJson);

  // kick off stateProof flow
  const validatedTransaction = stateProofHandler.runStateProof();

  console.log(`StateProof validation for ${transactionId} returned ${validatedTransaction}`);
  return validatedTransaction;
});
