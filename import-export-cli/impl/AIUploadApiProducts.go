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

	"github.com/spf13/cast"
	"github.com/wso2/product-apim-tooling/import-export-cli/credentials"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"
)

var apiProducts []utils.APIProduct

// Do the API exportation
func UploadAPIProductsAI(apiListQueue chan<- []map[string]interface{}) {
	fmt.Println("Uploading API Products..!")
	if count == 0 {
		fmt.Println("No APIs available to be exported..!")
	} else {
		var counterSuceededAPIs = 0
		for count > 0 {
			accessToken, preCommandErr := credentials.GetOAuthAccessToken(Credential, CmdUploadEnvironment)
			if preCommandErr == nil {
				apiList := []map[string]interface{}{}
				for i := startingApiIndexFromList; i < len(apiProducts); i++ {
					apiPayload := getAPIPayload(apiProducts[i], accessToken, CmdUploadEnvironment, true)
					if apiPayload != nil {
						apiList = append(apiList, apiPayload)
					}
					counterSuceededAPIs++
				}
				atomic.AddInt32(&totalAPIs, int32(len(apiList)))
				if len(apiList) > 0 {
					apiListQueue <- apiList
				}
			} else {
				fmt.Println("Error getting OAuth Tokens : " + preCommandErr.Error())
			}
			apiListOffset += utils.MaxAPIsToExportOnce
			count, apiProducts, _ = GetAPIProductListFromEnv(accessToken, CmdUploadEnvironment, "", strconv.Itoa(utils.MaxAPIsToExportOnce)+"&offset="+strconv.Itoa(apiListOffset))
			startingApiIndexFromList = 0
		}
		fmt.Println("\nTotal number of APIs processed: " + cast.ToString(counterSuceededAPIs))
	}
}
