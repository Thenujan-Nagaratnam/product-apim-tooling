/*
*  Copyright (c) WSO2 Inc. (http://www.wso2.org) All Rights Reserved.
*
*  WSO2 Inc. licenses this file to you under the Apache License,
*  Version 2.0 (the "License"); you may not use this file except
*  in compliance with the License.
*  You may obtain a copy of the License at
*
*    http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing,
* software distributed under the License is distributed on an
* "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
* KIND, either express or implied.  See the License for the
* specific language governing permissions and limitations
* under the License.
 */

package impl

import (
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/wso2/product-apim-tooling/import-export-cli/credentials"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"
)

var apiProducts []utils.APIProduct

// func UploadAPIProducts(credential credentials.Credential, cmdUploadEnvironment, authToken, endpointUrl string, uploadAll bool) {

// 	OnPremKey = authToken
// 	Endpoint = endpointUrl
// 	CmdUploadEnvironment = cmdUploadEnvironment
// 	Credential = credential
// 	publisherEndpoint := utils.GetPublisherEndpointOfEnv(cmdUploadEnvironment, utils.MainConfigFilePath)
// 	UploadAll = uploadAll
// 	UploadProducts = true

// 	fmt.Println("Uploading public APIs to vector DB...")

// 	accessToken, preCommandErr := credentials.GetOAuthAccessToken(credential, cmdUploadEnvironment)

// 	if preCommandErr != nil {
// 		utils.HandleErrorAndExit("Error getting access token", preCommandErr)
// 	}

// 	apiListQueue := make(chan []map[string]interface{}, 10)

// 	go ProduceAPIPayloads(accessToken, publisherEndpoint, apiListQueue)

// 	numConsumers := 3
// 	var wg sync.WaitGroup
// 	for i := 0; i < numConsumers; i++ {
// 		wg.Add(1)
// 		go ConsumeAPIPayloads(apiListQueue, &wg)
// 	}

// 	wg.Wait()

// 	fmt.Printf("\nTotal number of public APIs present in the API Manager: %d\nTotal number of APIs successfully uploaded: %d\n\n", totalAPIs, uploadedAPIs)
// }

// Do the API exportation
func ExportAPIProductsAI(tenant string, apiListQueue chan<- []map[string]interface{}) {
	if count == 0 {
		fmt.Println("No APIs available to be exported..!")
	} else {

		var counterSuceededAPIs = 0
		for count > 0 {
			accessToken, preCommandErr := credentials.GetOAuthAccessToken(Credential, CmdUploadEnvironment)
			if preCommandErr == nil {
				apiList := []map[string]interface{}{}
				for i := startingApiIndexFromList; i < len(apiProducts); i++ {
					apiPayload := getAPIPayload(apiProducts[i], accessToken, CmdUploadEnvironment, tenant, true)
					if apiPayload != nil {
						apiList = append(apiList, apiPayload)
						counterSuceededAPIs++
					}
				}
				atomic.AddInt32(&totalAPIs, int32(counterSuceededAPIs))
				apiListQueue <- apiList
			} else {
				fmt.Println("Error getting OAuth Tokens : " + preCommandErr.Error())
			}
			apiListOffset += utils.MaxAPIsToExportOnce
			count, apiProducts, _ = GetAPIProductListFromEnv(accessToken, CmdUploadEnvironment, "", "?limit="+strconv.Itoa(utils.MaxAPIsToExportOnce)+"&offset="+strconv.Itoa(apiListOffset))
			startingApiIndexFromList = 0
		}
	}
}
